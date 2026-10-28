package whatsapp

import (
	"context"
	"errors"
	"fmt"
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
	client     waClient
	creds      Credentials
	locale     string
	msgHandler func(context.Context, *im.IncomingMessage) error
	typing     *typingLoops

	startedAt   time.Time
	connectedAt atomic.Int64 // unix nanos of the first connect; 0 until then
}

func newAdapter(ctx context.Context, client waClient, creds Credentials, locale string,
	msgHandler func(context.Context, *im.IncomingMessage) error,
) *Adapter {
	a := &Adapter{
		ctx:        ctx,
		client:     client,
		creds:      creds,
		locale:     locale,
		msgHandler: msgHandler,
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

	content := im.FormatIMDisplayContent(reply.Content, im.StreamDisplayFinal)
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
// the quoted body is rebuilt from it.
func quoteContext(incoming *im.IncomingMessage) *waE2E.ContextInfo {
	ci := &waE2E.ContextInfo{
		StanzaID:      proto.String(incoming.MessageID),
		QuotedMessage: &waE2E.Message{Conversation: proto.String(incoming.Content)},
	}
	if sender := incoming.Extra["sender_jid"]; sender != "" {
		ci.Participant = proto.String(sender)
	}
	return ci
}

func (a *Adapter) sendChatPresence(chat types.JID, state types.ChatPresence) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := a.client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
		logger.Debugf(ctx, "[WhatsApp] Chat presence %s to %s: %v", state, chat, err)
	}
}

// close stops all typing loops. Called when the channel stops.
func (a *Adapter) close() {
	a.typing.StopAll()
}
