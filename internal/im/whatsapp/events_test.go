package whatsapp

import (
	"context"
	"fmt"
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
		{"unresolvable lid", func(e *events.Message) {
			e.Info.Sender, e.Info.Chat = lidJID("999"), lidJID("999")
		}},
		{"audio", func(e *events.Message) {
			e.Message = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}
		}},
		{"video", func(e *events.Message) {
			e.Message = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("x")}}
		}},
		{"sticker", func(e *events.Message) {
			e.Message = &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}
		}},
		{"stranger image", func(e *events.Message) {
			e.Info.Sender, e.Info.Chat = pnJID(testStranger), pnJID(testStranger)
			e.Message = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}
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

func TestToIncomingResolvesLID(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	c.lids = map[string]string{"111222": testFriend}

	viaPN, ok := a.toIncoming(textEvent(pnJID(testFriend), "hi"))
	if !ok {
		t.Fatal("PN sender dropped")
	}

	withAlt := textEvent(lidJID("555666"), "hi")
	withAlt.Info.SenderAlt = pnJID(testOwner)
	msg, ok := a.toIncoming(withAlt)
	if !ok || msg.UserID != testOwner {
		t.Fatalf("SenderAlt: msg=%+v ok=%v", msg, ok)
	}
	if msg.Extra["sender_jid"] != "555666@lid" || msg.Extra["chat_jid"] != "555666@lid" {
		t.Fatalf("original JIDs not kept: %v", msg.Extra)
	}

	viaLID, ok := a.toIncoming(textEvent(lidJID("111222"), "hi"))
	if !ok {
		t.Fatal("LID sender with store mapping dropped")
	}
	if viaLID.UserID != viaPN.UserID {
		t.Fatalf("UserID via LID = %q, via PN = %q; want the same", viaLID.UserID, viaPN.UserID)
	}

	stranger := textEvent(lidJID("777"), "hi")
	c.lids["777"] = testStranger
	if _, ok := a.toIncoming(stranger); ok {
		t.Fatal("stranger resolved via LID must still be dropped")
	}
}

func TestQuoteRecognisesBotLID(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	c.ownLID = lidJID("424242")
	e := textEvent(pnJID(testOwner), "")
	e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("?"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("Q1"),
			Participant:   proto.String("424242@lid"),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("bot said")},
		},
	}}
	msg, ok := a.toIncoming(e)
	if !ok || !msg.Quote.IsBotMessage {
		t.Fatalf("quote of bot LID not recognised: %+v", msg)
	}
}

func TestToIncomingMapsImage(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	for _, tt := range []struct {
		name, caption, mime, wantName, wantQuote string
	}{
		{"with caption", "what is this?", "image/png", "MSG1.png", "what is this?"},
		{"no caption, unknown mime", "", "", "MSG1.jpg", "📷 Photo"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := textEvent(pnJID(testOwner), "")
			img := &waE2E.ImageMessage{FileLength: proto.Uint64(1234)}
			if tt.caption != "" {
				img.Caption = proto.String(tt.caption)
			}
			if tt.mime != "" {
				img.Mimetype = proto.String(tt.mime)
			}
			e.Message = &waE2E.Message{ImageMessage: img}
			msg, ok := a.toIncoming(e)
			if !ok {
				t.Fatal("image dropped")
			}
			if msg.MessageType != im.MessageTypeImage || msg.Content != tt.caption || msg.FileName != tt.wantName ||
				msg.FileSize != 1234 || msg.FileKey != "MSG1" || msg.Extra["quote_text"] != tt.wantQuote {
				t.Fatalf("msg = %+v", msg)
			}
			if got, ok := a.media.get("MSG1"); !ok || got != img {
				t.Fatal("image proto not cached")
			}
		})
	}
}

func TestToIncomingMapsDocument(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	for _, tt := range []struct {
		name     string
		msg      *waE2E.Message
		wantName string
		caption  string
	}{
		{
			"plain", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
				FileName: proto.String("Q3 report.pdf"), Mimetype: proto.String("application/pdf"),
			}},
			"Q3 report.pdf", "",
		},
		{
			"caption wrapper", &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
				Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
					FileName: proto.String("notes"), Mimetype: proto.String("text/plain"),
					Caption: proto.String("summarise"),
				}},
			}},
			"notes.txt", "summarise",
		},
		{
			"no name", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Mimetype: proto.String("application/pdf")}},
			"MSG1.pdf", "",
		},
		{
			"path in name", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("../../etc/x.pdf")}},
			"x.pdf", "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := textEvent(pnJID(testFriend), "")
			e.Message = tt.msg
			msg, ok := a.toIncoming(e)
			if !ok {
				t.Fatal("document dropped")
			}
			if msg.MessageType != im.MessageTypeFile || msg.FileName != tt.wantName || msg.Content != tt.caption ||
				msg.FileKey != "MSG1" {
				t.Fatalf("msg = %+v", msg)
			}
		})
	}
}

func TestTerminalEventsStopChannel(t *testing.T) {
	for _, evt := range []any{&events.LoggedOut{}, &events.StreamReplaced{}} {
		t.Run(fmt.Sprintf("%T", evt), func(t *testing.T) {
			a, _ := newTestAdapter(t, nil)
			a.typing.Start(testChat)
			a.handleEvent(evt)
			if a.ctx.Err() == nil {
				t.Fatal("adapter context not cancelled")
			}
			a.typing.mu.Lock()
			n := len(a.typing.loops)
			a.typing.mu.Unlock()
			if n != 0 {
				t.Fatal("typing loops still running")
			}
		})
	}
}

func TestTransientEventsKeepRunning(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	a.handleEvent(&events.TemporaryBan{})
	a.handleEvent(&events.ConnectFailure{})
	if a.ctx.Err() != nil {
		t.Fatal("transient failure stopped the channel")
	}
}
