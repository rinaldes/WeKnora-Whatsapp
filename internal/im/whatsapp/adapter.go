package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var errNoCallbacks = errors.New("whatsapp does not use HTTP callbacks")

// sendTimeout bounds one outgoing message or presence update.
const sendTimeout = 30 * time.Second

// Adapter is the WhatsApp im.Adapter. It has no StreamSender, so the IM
// service collects the full answer and calls SendReply once.
type Adapter struct {
	ctx        context.Context // adapter lifetime; cancelled when the channel stops
	stop       context.CancelFunc
	client     waClient
	creds      Credentials
	locale     string
	msgHandler func(context.Context, *im.IncomingMessage) error
	typing     *typingLoops
	media      *mediaCache

	startedAt   time.Time
	connectedAt atomic.Int64 // unix nanos of the first connect; 0 until then
}

func newAdapter(ctx context.Context, stop context.CancelFunc, client waClient, creds Credentials, locale string,
	msgHandler func(context.Context, *im.IncomingMessage) error,
) *Adapter {
	a := &Adapter{
		ctx:        ctx,
		stop:       stop,
		client:     client,
		creds:      creds,
		locale:     locale,
		msgHandler: msgHandler,
		media:      newMediaCache(),
		startedAt:  time.Now(),
	}
	a.typing = newTypingLoops(a.sendChatPresence)
	return a
}

func (a *Adapter) Platform() im.Platform { return im.PlatformWhatsApp }

func (a *Adapter) VerifyCallback(*gin.Context) error { return errNoCallbacks }

func (a *Adapter) ParseCallback(*gin.Context) (*im.IncomingMessage, error) {
	return nil, errNoCallbacks
}

func (a *Adapter) HandleURLVerification(*gin.Context) bool { return false }

// SendReply stops "typing…" and sends the reply as one or more text messages.
// The first one quotes the user's message.
func (a *Adapter) SendReply(ctx context.Context, incoming *im.IncomingMessage, reply *im.ReplyMessage) error {
	chat, err := types.ParseJID(incoming.Extra["chat_jid"])
	if err != nil || chat.IsEmpty() {
		return fmt.Errorf("whatsapp reply: invalid chat_jid %q", incoming.Extra["chat_jid"])
	}
	a.typing.Stop(chat)

	content := localizeCoreNotice(reply.Content, a.locale)
	content = im.FormatIMDisplayContent(content, im.StreamDisplayFinal)
	chunks := Chunk(ToWhatsApp(content), maxChunkRunes)
	for i, text := range chunks {
		msg := &waE2E.ExtendedTextMessage{Text: proto.String(text)}
		if i == 0 && incoming.MessageID != "" {
			msg.ContextInfo = quoteContext(incoming)
		}
		if err := a.send(ctx, chat, &waE2E.Message{ExtendedTextMessage: msg}); err != nil {
			return err
		}
	}
	return nil
}

// send delivers one message, retrying once on failure.
func (a *Adapter) send(ctx context.Context, chat types.JID, msg *waE2E.Message) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, err = a.client.SendMessage(sendCtx, chat, msg)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		logger.Warnf(ctx, "[WhatsApp] Send to %s failed (attempt %d): %v", chat, attempt+1, err)
	}
	return fmt.Errorf("whatsapp send: %w", err)
}

// quoteContext quotes the user's message. Only its text is known here, so
// the quoted body is rebuilt from it. Core rewrites Content for attachments,
// so the original text is read from Extra.
func quoteContext(incoming *im.IncomingMessage) *waE2E.ContextInfo {
	text, ok := incoming.Extra["quote_text"]
	if !ok {
		text = incoming.Content
	}
	ci := &waE2E.ContextInfo{
		StanzaID:      proto.String(incoming.MessageID),
		QuotedMessage: &waE2E.Message{Conversation: proto.String(text)},
	}
	if sender := incoming.Extra["sender_jid"]; sender != "" {
		ci.Participant = proto.String(sender)
	}
	return ci
}

// DownloadFile implements im.FileDownloader for images and documents. The
// file is used only as a QA attachment; WhatsApp channels never save to a
// knowledge base.
func (a *Adapter) DownloadFile(ctx context.Context, msg *im.IncomingMessage) (io.ReadCloser, string, error) {
	media, ok := a.media.get(msg.FileKey)
	if !ok {
		return nil, "", fmt.Errorf("whatsapp media %s expired", msg.FileKey)
	}
	data, err := a.client.Download(ctx, media)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp download: %w", err)
	}
	return io.NopCloser(bytes.NewReader(data)), msg.FileName, nil
}

func (a *Adapter) sendChatPresence(chat types.JID, state types.ChatPresence) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := a.client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
		logger.Debugf(ctx, "[WhatsApp] Chat presence %s to %s: %v", state, chat, err)
	}
}

// close stops the connection and all typing loops. Called when the channel
// stops, or by the adapter itself when the session is gone for good.
func (a *Adapter) close() {
	a.stop()
	a.typing.StopAll()
}
