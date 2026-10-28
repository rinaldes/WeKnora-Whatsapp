package whatsapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

type fakePairClient struct {
	qr       chan whatsmeow.QRChannelItem
	mu       sync.Mutex
	handlers []whatsmeow.EventHandler
	jid      string

	disconnects int
	deletes     int
}

func newFakePairClient() *fakePairClient {
	return &fakePairClient{qr: make(chan whatsmeow.QRChannelItem, 8)}
}

func (f *fakePairClient) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	// Mirror whatsmeow: the channel closes when ctx ends.
	out := make(chan whatsmeow.QRChannelItem)
	go func() {
		defer close(out)
		for {
			select {
			case item, ok := <-f.qr:
				if !ok {
					return
				}
				out <- item
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (f *fakePairClient) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, h)
	return uint32(len(f.handlers))
}

func (f *fakePairClient) emit(evt any) {
	f.mu.Lock()
	hs := append([]whatsmeow.EventHandler(nil), f.handlers...)
	f.mu.Unlock()
	for _, h := range hs {
		h(evt)
	}
}

func (f *fakePairClient) Connect() error { return nil }

func (f *fakePairClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
}

func (f *fakePairClient) DeviceJID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jid
}

func (f *fakePairClient) DeleteDevice(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return nil
}

func (f *fakePairClient) counts() (disconnects, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnects, f.deletes
}

func code(c string) whatsmeow.QRChannelItem {
	return whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: c}
}

func managerWith(clients ...*fakePairClient) *PairingManager {
	i := 0
	m := newPairingManager(NewStore(nil), func(context.Context) (pairClient, error) {
		c := clients[i]
		i++
		return c, nil
	})
	m.settle = 50 * time.Millisecond
	return m
}

func waitStatus(t *testing.T, m *PairingManager, id string, want PairingStatus) PairingResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		res, ok := m.Status(id)
		if !ok {
			t.Fatalf("Status(%s) not found", id)
		}
		if res.Status == want {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want %s", res, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPairingSuccess(t *testing.T) {
	c := newFakePairClient()
	m := managerWith(c)
	c.qr <- code("qr-1")

	id, first, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if first.Status != PairingPending || first.QRCode != "qr-1" {
		t.Fatalf("first = %+v, want pending qr-1", first)
	}

	c.qr <- code("qr-2")
	deadline := time.Now().Add(time.Second)
	for res, _ := m.Status(id); res.QRCode != "qr-2"; res, _ = m.Status(id) {
		if time.Now().After(deadline) {
			t.Fatalf("QR code not rotated: %+v", res)
		}
		time.Sleep(5 * time.Millisecond)
	}

	c.mu.Lock()
	c.jid = "6281234567890:12@s.whatsapp.net"
	c.mu.Unlock()
	c.qr <- whatsmeow.QRChannelSuccess
	c.emit(&events.Connected{})

	res := waitStatus(t, m, id, PairingSuccess)
	if res.DeviceJID != "6281234567890:12@s.whatsapp.net" || res.QRCode != "" {
		t.Fatalf("result = %+v", res)
	}
	time.Sleep(20 * time.Millisecond)
	if d, del := c.counts(); d == 0 || del != 0 {
		t.Fatalf("disconnects=%d deletes=%d, want >0 and 0", d, del)
	}
	if got := m.store.RecentlyPaired(); len(got) != 1 || got[0] != res.DeviceJID {
		t.Fatalf("RecentlyPaired() = %v", got)
	}
}

func TestPairingTimeoutDeletesDevice(t *testing.T) {
	c := newFakePairClient()
	m := managerWith(c)
	c.qr <- code("qr-1")

	id, _, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	c.qr <- whatsmeow.QRChannelTimeout
	waitStatus(t, m, id, PairingTimeout)
	time.Sleep(20 * time.Millisecond)
	if d, del := c.counts(); d == 0 || del != 1 {
		t.Fatalf("disconnects=%d deletes=%d, want >0 and 1", d, del)
	}
	if len(m.store.RecentlyPaired()) != 0 {
		t.Fatal("timed out pairing must not be recent")
	}
}

func TestPairingTTLExpiry(t *testing.T) {
	c := newFakePairClient()
	m := managerWith(c)
	m.ttl = 50 * time.Millisecond
	c.qr <- code("qr-1")

	id, _, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitStatus(t, m, id, PairingTimeout)
	time.Sleep(20 * time.Millisecond)
	if _, del := c.counts(); del != 1 {
		t.Fatalf("deletes=%d, want 1", del)
	}
}

func TestPairingErrorEvent(t *testing.T) {
	c := newFakePairClient()
	m := managerWith(c)
	c.qr <- code("qr-1")
	id, _, _ := m.Start(context.Background())
	c.qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: errors.New("boom")}
	res := waitStatus(t, m, id, PairingError)
	if res.Error != "boom" {
		t.Fatalf("Error = %q", res.Error)
	}
}

func TestPairingCap(t *testing.T) {
	clients := make([]*fakePairClient, maxInflightPairings+1)
	for i := range clients {
		clients[i] = newFakePairClient()
		clients[i].qr <- code("qr")
	}
	m := managerWith(clients...)
	for i := 0; i < maxInflightPairings; i++ {
		if _, _, err := m.Start(context.Background()); err != nil {
			t.Fatalf("Start() #%d error = %v", i, err)
		}
	}
	if _, _, err := m.Start(context.Background()); !errors.Is(err, ErrTooManyPairings) {
		t.Fatalf("Start() past cap error = %v, want ErrTooManyPairings", err)
	}
}

func TestPairingUnknownID(t *testing.T) {
	m := managerWith()
	if _, ok := m.Status("nope"); ok {
		t.Fatal("Status(unknown) ok = true")
	}
}
