package whatsapp

import (
	"context"
	"path/filepath"
	"strings"
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

	msg := &im.IncomingMessage{
		Platform:  im.PlatformWhatsApp,
		UserID:    pn,
		UserName:  info.PushName,
		ChatType:  im.ChatTypeDirect,
		MessageID: info.ID,
		Extra: map[string]string{
			"chat_jid":   info.Chat.String(),
			"sender_jid": info.Sender.String(),
		},
	}
	m := evt.Message
	if doc := m.GetDocumentWithCaptionMessage().GetMessage().GetDocumentMessage(); doc != nil {
		m = &waE2E.Message{DocumentMessage: doc} // whatsmeow usually unwraps this already
	}
	var ctxInfo *waE2E.ContextInfo
	switch {
	case m.GetConversation() != "":
		msg.MessageType = im.MessageTypeText
		msg.Content = m.GetConversation()
	case m.GetExtendedTextMessage().GetText() != "":
		ext := m.GetExtendedTextMessage()
		msg.MessageType = im.MessageTypeText
		msg.Content = ext.GetText()
		ctxInfo = ext.GetContextInfo()
	case m.GetImageMessage() != nil:
		img := m.GetImageMessage()
		msg.MessageType = im.MessageTypeImage
		msg.Content = img.GetCaption()
		msg.FileName = info.ID + extForMIME(img.GetMimetype(), ".jpg")
		msg.FileSize = int64(img.GetFileLength())
		ctxInfo = img.GetContextInfo()
		a.media.put(info.ID, img)
	case m.GetDocumentMessage() != nil:
		doc := m.GetDocumentMessage()
		msg.MessageType = im.MessageTypeFile
		msg.Content = doc.GetCaption()
		msg.FileName = documentFileName(doc, info.ID)
		msg.FileSize = int64(doc.GetFileLength())
		ctxInfo = doc.GetContextInfo()
		a.media.put(info.ID, doc)
	default: // audio, video, sticker, location, contact, reaction, poll, ...
		return nil, false
	}
	if msg.MessageType != im.MessageTypeText {
		// A non-empty FileKey marks the message as an attachment to core.
		msg.FileKey = info.ID
	}
	msg.Extra["quote_text"] = quoteText(msg)
	msg.Quote = a.quoteFrom(ctxInfo)
	return msg, true
}

// documentFileName is the sender's file name, or one built from the message
// ID and MIME type.
func documentFileName(doc *waE2E.DocumentMessage, id string) string {
	name := strings.TrimSpace(filepath.Base(doc.GetFileName()))
	if name != "" && name != "." && name != "/" {
		if filepath.Ext(name) == "" {
			name += extForMIME(doc.GetMimetype(), "")
		}
		return name
	}
	return id + extForMIME(doc.GetMimetype(), ".bin")
}

// quoteText is what a reply shows as the quoted user message.
func quoteText(msg *im.IncomingMessage) string {
	if msg.Content != "" {
		return msg.Content
	}
	switch msg.MessageType {
	case im.MessageTypeImage:
		return "📷 Photo"
	case im.MessageTypeFile:
		return "📄 " + msg.FileName
	}
	return ""
}

// resolveSenderPN returns the sender's phone number (digits only), whether
// WhatsApp addressed them by phone number or by LID. The allowlist and the
// WeKnora session both key on it, so they don't change with the address form.
func (a *Adapter) resolveSenderPN(info types.MessageInfo) (string, bool) {
	if info.Sender.Server == types.DefaultUserServer && info.Sender.User != "" {
		return info.Sender.User, true
	}
	if info.SenderAlt.Server == types.DefaultUserServer && info.SenderAlt.User != "" {
		return info.SenderAlt.User, true
	}
	if info.Sender.Server == types.HiddenUserServer {
		ctx, cancel := context.WithTimeout(a.ctx, sendTimeout)
		defer cancel()
		pn, err := a.client.PNForLID(ctx, info.Sender.ToNonAD())
		if err == nil && pn.User != "" {
			return pn.User, true
		}
		logger.Debugf(ctx, "[WhatsApp] No phone number for LID %s (err=%v); dropping", info.Sender.User, err)
	}
	return "", false
}

// isBot reports whether jid (PN or LID form) is the bot's own account.
func (a *Adapter) isBot(jid types.JID) bool {
	switch jid.Server {
	case types.DefaultUserServer:
		return jid.User != "" && jid.User == a.creds.BotNumber()
	case types.HiddenUserServer:
		own := a.client.OwnLID()
		return jid.User != "" && jid.User == own.User
	}
	return false
}

// quoteFrom maps a reply's quoted message, if any.
func (a *Adapter) quoteFrom(ci *waE2E.ContextInfo) *im.QuotedMessage {
	if ci.GetStanzaID() == "" {
		return nil
	}
	q := &im.QuotedMessage{MessageID: ci.GetStanzaID()}
	if p, err := types.ParseJID(ci.GetParticipant()); err == nil {
		q.SenderID = p.User
		q.IsBotMessage = a.isBot(p)
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
