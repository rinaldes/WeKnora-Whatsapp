package whatsapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func pnJID(user string) types.JID  { return types.NewJID(user, types.DefaultUserServer) }
func lidJID(user string) types.JID { return types.NewJID(user, types.HiddenUserServer) }

// textEvent builds a DM from sender with the given body.
func textEvent(sender types.JID, body string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: sender, Sender: sender},
			ID:            "MSG1",
			PushName:      "Alice",
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: proto.String(body)},
	}
}

func TestToIncomingDrops(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	for _, tt := range []struct {
		name string
		mod  func(e *events.Message)
	}{
		{"from me", func(e *events.Message) { e.Info.IsFromMe = true }},
		{"group", func(e *events.Message) {
			e.Info.IsGroup = true
			e.Info.Chat = types.NewJID("123", types.GroupServer)
		}},
		{"status broadcast", func(e *events.Message) { e.Info.Chat = types.StatusBroadcastJID }},
		{"newsletter", func(e *events.Message) { e.Info.Chat = types.NewJID("1", types.NewsletterServer) }},
		{"old backlog", func(e *events.Message) { e.Info.Timestamp = time.Now().Add(-time.Hour) }},
		{"stranger", func(e *events.Message) {
			e.Info.Sender, e.Info.Chat = pnJID(testStranger), pnJID(testStranger)
		}},
		{"edit", func(e *events.Message) { e.IsEdit = true }},
		{"non text", func(e *events.Message) {
			e.Message = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}
		}},
		{"reaction", func(e *events.Message) {
			e.Message = &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := textEvent(pnJID(testOwner), "hi")
			tt.mod(e)
			if msg, ok := a.toIncoming(e); ok {
				t.Fatalf("toIncoming() = %+v, want dropped", msg)
			}
		})
	}
}

func TestToIncomingMapsText(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	msg, ok := a.toIncoming(textEvent(pnJID(testFriend), "hello"))
	if !ok {
		t.Fatal("friend message dropped")
	}
	if msg.Platform != im.PlatformWhatsApp || msg.MessageType != im.MessageTypeText ||
		msg.UserID != testFriend || msg.UserName != "Alice" || msg.ChatID != "" ||
		msg.ChatType != im.ChatTypeDirect || msg.Content != "hello" || msg.MessageID != "MSG1" {
		t.Fatalf("mapped = %+v", msg)
	}
	if msg.Extra["chat_jid"] != testFriend+"@s.whatsapp.net" || msg.Extra["sender_jid"] != testFriend+"@s.whatsapp.net" {
		t.Fatalf("extra = %v", msg.Extra)
	}
	if msg.Quote != nil {
		t.Fatalf("unexpected quote %+v", msg.Quote)
	}
}

func TestToIncomingMapsQuote(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	e := textEvent(pnJID(testOwner), "")
	e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("what about this?"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("Q1"),
			Participant:   proto.String("6280000000000@s.whatsapp.net"),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("earlier answer")},
		},
	}}
	msg, ok := a.toIncoming(e)
	if !ok {
		t.Fatal("dropped")
	}
	q := msg.Quote
	if msg.Content != "what about this?" || q == nil || q.MessageID != "Q1" || q.Content != "earlier answer" ||
		q.SenderID != "6280000000000" || !q.IsBotMessage {
		t.Fatalf("msg = %+v quote = %+v", msg, q)
	}

	e.Message.ExtendedTextMessage.ContextInfo.QuotedMessage = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}
	e.Message.ExtendedTextMessage.ContextInfo.Participant = proto.String(testFriend + "@s.whatsapp.net")
	msg, _ = a.toIncoming(e)
	if msg.Quote.NonTextType != "image" || msg.Quote.IsBotMessage {
		t.Fatalf("quote = %+v", msg.Quote)
	}
}

func TestHandleMessageMarksReadTypesAndDispatches(t *testing.T) {
	got := make(chan *im.IncomingMessage, 1)
	a, c := newTestAdapter(t, func(_ context.Context, m *im.IncomingMessage) error {
		got <- m
		return nil
	})
	a.handleEvent(textEvent(pnJID(testOwner), "hi"))
	select {
	case m := <-got:
		if m.Content != "hi" {
			t.Fatalf("dispatched %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("message not dispatched")
	}
	// "composing" comes from the typing goroutine; give it a moment.
	deadline := time.Now().Add(time.Second)
	for !strings.HasPrefix(strings.Join(c.Calls(), "|"), "read:MSG1|presence:composing") {
		if time.Now().After(deadline) {
			t.Fatalf("calls = %v", c.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}

	a.handleEvent(textEvent(pnJID(testStranger), "hi"))
	select {
	case m := <-got:
		t.Fatalf("stranger dispatched: %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConnectedSendsPresenceAndSetsCutoff(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	a.handleEvent(&events.Connected{})
	first := a.connectedAt.Load()
	if first == 0 || !strings.Contains(strings.Join(c.Calls(), "|"), "available") {
		t.Fatalf("connectedAt=%d calls=%v", first, c.Calls())
	}
	time.Sleep(time.Millisecond)
	a.handleEvent(&events.Connected{}) // reconnect keeps the first time
	if a.connectedAt.Load() != first {
		t.Fatal("reconnect moved the history cutoff")
	}
}
