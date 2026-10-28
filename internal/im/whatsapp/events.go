package whatsapp

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// historyGrace is how far before the first connect a message may be dated
// and still be answered. Older messages are backlog, not new requests.
const historyGrace = 2 * time.Minute

// handleEvent is registered with whatsmeow, which calls it synchronously.
func (a *Adapter) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		a.connectedAt.CompareAndSwap(0, time.Now().UnixNano())
		ctx, cancel := context.WithTimeout(a.ctx, sendTimeout)
		defer cancel()
		// Chat presence ("typing…") is only shown while we're available.
		if err := a.client.SendPresence(ctx, types.PresenceAvailable); err != nil {
			logger.Warnf(ctx, "[WhatsApp] Send presence: %v", err)
		}
	case *events.Message:
		a.handleMessage(e)
	}
}

func (a *Adapter) handleMessage(evt *events.Message) {
	msg, ok := a.toIncoming(evt)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, sendTimeout)
	if err := a.client.MarkRead(ctx, []types.MessageID{evt.Info.ID}, evt.Info.Timestamp,
		evt.Info.Chat, evt.Info.Sender); err != nil {
		logger.Debugf(ctx, "[WhatsApp] Mark read: %v", err)
	}
	cancel()
	a.typing.Start(evt.Info.Chat)

	// HandleMessage does session/DB work before enqueuing QA; don't hold up
	// whatsmeow's event loop with it.
	go func() {
		if err := a.msgHandler(a.ctx, msg); err != nil {
			logger.Errorf(a.ctx, "[WhatsApp] Handle message %s: %v", msg.MessageID, err)
		}
	}()
}

// historyCutoff is the oldest timestamp a message may have to be answered.
func (a *Adapter) historyCutoff() time.Time {
	base := a.startedAt
	if ns := a.connectedAt.Load(); ns != 0 {
		base = time.Unix(0, ns)
	}
	return base.Add(-historyGrace)
}

// toIncoming filters and maps a WhatsApp message. It returns false for
// anything the bot must ignore: own messages, groups, broadcasts and
// newsletters, backlog, senders not on the allowlist, and unsupported types.
func (a *Adapter) toIncoming(evt *events.Message) (*im.IncomingMessage, bool) {
	info := evt.Info
	if info.IsFromMe || info.IsGroup || evt.IsEdit {
		return nil, false
	}
	switch info.Chat.Server {
	case types.DefaultUserServer, types.HiddenUserServer:
	default: // groups, status/broadcast, newsletters, ...
		return nil, false
	}
	if info.Timestamp.Before(a.historyCutoff()) {
		return nil, false
	}
	pn, ok := a.resolveSenderPN(info)
	if !ok || !a.creds.Allowed(pn) {
		return nil, false
	}

	m := evt.Message
	text := m.GetConversation()
	var ctxInfo *waE2E.ContextInfo
	if ext := m.GetExtendedTextMessage(); ext != nil {
		if text == "" {
			text = ext.GetText()
		}
		ctxInfo = ext.GetContextInfo()
	}
	if text == "" {
		return nil, false
	}

	return &im.IncomingMessage{
		Platform:    im.PlatformWhatsApp,
		MessageType: im.MessageTypeText,
		UserID:      pn,
		UserName:    info.PushName,
		ChatType:    im.ChatTypeDirect,
		Content:     text,
		MessageID:   info.ID,
		Quote:       a.quoteFrom(ctxInfo),
		Extra: map[string]string{
			"chat_jid":   info.Chat.String(),
			"sender_jid": info.Sender.String(),
		},
	}, true
}

// resolveSenderPN returns the sender's phone number (digits only).
func (a *Adapter) resolveSenderPN(info types.MessageInfo) (string, bool) {
	if info.Sender.Server == types.DefaultUserServer && info.Sender.User != "" {
		return info.Sender.User, true
	}
	return "", false
}

// quoteFrom maps a reply's quoted message, if any.
func (a *Adapter) quoteFrom(ci *waE2E.ContextInfo) *im.QuotedMessage {
	if ci.GetStanzaID() == "" {
		return nil
	}
	q := &im.QuotedMessage{MessageID: ci.GetStanzaID()}
	if p, err := types.ParseJID(ci.GetParticipant()); err == nil {
		q.SenderID = p.User
		q.IsBotMessage = p.User != "" && p.User == a.creds.BotNumber()
	}
	quoted := ci.GetQuotedMessage()
	if text := quoted.GetConversation(); text != "" {
		q.Content = text
	} else if text := quoted.GetExtendedTextMessage().GetText(); text != "" {
		q.Content = text
	} else {
		q.NonTextType = nonTextType(quoted)
	}
	return q
}

func nonTextType(m *waE2E.Message) string {
	switch {
	case m.GetImageMessage() != nil:
		return "image"
	case m.GetDocumentMessage() != nil, m.GetDocumentWithCaptionMessage() != nil:
		return "file"
	case m.GetAudioMessage() != nil:
		return "audio"
	case m.GetVideoMessage() != nil:
		return "video"
	case m.GetStickerMessage() != nil:
		return "sticker"
	default:
		return "message"
	}
}
