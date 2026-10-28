package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// fakeClient records what the adapter sends.
type fakeClient struct {
	mu        sync.Mutex
	calls     []string // "send:<text>", "presence:<state>", "read:<id>", "available"
	sent      []*waE2E.Message
	sendErrs  []error // consumed in order by SendMessage
	connected bool
	lids      map[string]string // LID user → PN user
	ownLID    types.JID
}

func (f *fakeClient) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeClient) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeClient) Connect() error    { f.connected = true; return nil }
func (f *fakeClient) Disconnect()       { f.connected = false }
func (f *fakeClient) IsConnected() bool { return f.connected }

func (f *fakeClient) SendMessage(_ context.Context, _ types.JID, msg *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.mu.Lock()
	var err error
	if len(f.sendErrs) > 0 {
		err, f.sendErrs = f.sendErrs[0], f.sendErrs[1:]
	}
	if err == nil {
		f.sent = append(f.sent, msg)
	}
	f.mu.Unlock()
	f.log("send:" + msg.GetExtendedTextMessage().GetText())
	return whatsmeow.SendResponse{}, err
}

func (f *fakeClient) SendChatPresence(_ context.Context, _ types.JID, state types.ChatPresence, _ types.ChatPresenceMedia) error {
	f.log("presence:" + string(state))
	return nil
}

func (f *fakeClient) SendPresence(context.Context, types.Presence) error {
	f.log("available")
	return nil
}

func (f *fakeClient) PNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if pn, ok := f.lids[lid.User]; ok {
		return types.NewJID(pn, types.DefaultUserServer), nil
	}
	return types.EmptyJID, nil
}

func (f *fakeClient) OwnLID() types.JID { return f.ownLID }

func (f *fakeClient) MarkRead(_ context.Context, ids []types.MessageID, _ time.Time, _, _ types.JID, _ ...types.ReceiptType) error {
	f.log("read:" + strings.Join(ids, ","))
	return nil
}

const (
	testBotJID   = "6280000000000:7@s.whatsapp.net"
	testOwner    = "6281111111111"
	testFriend   = "6282222222222"
	testStranger = "6283333333333"
)

func newTestAdapter(t *testing.T, handler func(context.Context, *im.IncomingMessage) error) (*Adapter, *fakeClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeClient{}
	if handler == nil {
		handler = func(context.Context, *im.IncomingMessage) error { return nil }
	}
	a := newAdapter(ctx, client, Credentials{
		DeviceJID:      testBotJID,
		OwnerNumber:    testOwner,
		AllowedNumbers: []string{testFriend},
	}, "", handler)
	t.Cleanup(func() {
		cancel()
		a.close()
	})
	return a, client
}

func testIncoming() *im.IncomingMessage {
	return &im.IncomingMessage{
		MessageID: "MSG1",
		Content:   "hi",
		Extra: map[string]string{
			"chat_jid":   testOwner + "@s.whatsapp.net",
			"sender_jid": testOwner + "@s.whatsapp.net",
		},
	}
}

func TestSendReplyChunksQuoteFirstOnly(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	long := strings.Repeat("a", 3000) + "\n\n" + strings.Repeat("b", 3000)
	if err := a.SendReply(context.Background(), testIncoming(), &im.ReplyMessage{Content: long}); err != nil {
		t.Fatalf("SendReply() error = %v", err)
	}
	if len(c.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(c.sent))
	}
	if !strings.HasPrefix(c.sent[0].GetExtendedTextMessage().GetText(), "aaa") ||
		!strings.HasPrefix(c.sent[1].GetExtendedTextMessage().GetText(), "bbb") {
		t.Fatal("chunks out of order")
	}
	q := c.sent[0].GetExtendedTextMessage().GetContextInfo()
	if q.GetStanzaID() != "MSG1" || q.GetParticipant() != testOwner+"@s.whatsapp.net" ||
		q.GetQuotedMessage().GetConversation() != "hi" {
		t.Fatalf("first chunk quote = %v", q)
	}
	if c.sent[1].GetExtendedTextMessage().GetContextInfo() != nil {
		t.Fatal("second chunk must not quote")
	}
}

func TestSendReplyFormatsAndStripsThink(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	reply := &im.ReplyMessage{Content: "<think>hmm</think>**Answer**"}
	if err := a.SendReply(context.Background(), testIncoming(), reply); err != nil {
		t.Fatal(err)
	}
	if got := c.sent[0].GetExtendedTextMessage().GetText(); got != "*Answer*" {
		t.Fatalf("text = %q", got)
	}
}

func TestSendReplyRetriesOnce(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	c.sendErrs = []error{errors.New("transient")}
	if err := a.SendReply(context.Background(), testIncoming(), &im.ReplyMessage{Content: "ok"}); err != nil {
		t.Fatalf("SendReply() error = %v, want retry success", err)
	}

	c.sendErrs = []error{errors.New("e1"), errors.New("e2")}
	if err := a.SendReply(context.Background(), testIncoming(), &im.ReplyMessage{Content: "ok"}); err == nil {
		t.Fatal("SendReply() error = nil after two failures")
	}
}

func TestSendReplyStopsTypingFirst(t *testing.T) {
	a, c := newTestAdapter(t, nil)
	chat := types.NewJID(testOwner, types.DefaultUserServer)
	a.typing.Start(chat)
	if err := a.SendReply(context.Background(), testIncoming(), &im.ReplyMessage{Content: "done"}); err != nil {
		t.Fatal(err)
	}
	calls := c.Calls()
	want := []string{"presence:composing", "presence:paused", "send:done"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSendReplyRejectsMissingChat(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	if err := a.SendReply(context.Background(), &im.IncomingMessage{}, &im.ReplyMessage{Content: "x"}); err == nil {
		t.Fatal("SendReply() without chat_jid error = nil")
	}
}

func TestCallbacksDisabled(t *testing.T) {
	a, _ := newTestAdapter(t, nil)
	if a.Platform() != im.PlatformWhatsApp {
		t.Fatalf("Platform() = %q", a.Platform())
	}
	if a.VerifyCallback(nil) == nil || a.HandleURLVerification(nil) {
		t.Fatal("callbacks must be disabled")
	}
	if _, err := a.ParseCallback(nil); err == nil {
		t.Fatal("ParseCallback() error = nil")
	}
}
