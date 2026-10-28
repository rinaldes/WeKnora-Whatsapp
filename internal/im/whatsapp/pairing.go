package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

// PairingStatus is the state of a QR pairing session.
type PairingStatus string

const (
	PairingPending PairingStatus = "pending"
	PairingSuccess PairingStatus = "success"
	PairingTimeout PairingStatus = "timeout"
	PairingError   PairingStatus = "error"
)

const (
	// pairingTTL bounds how long a QR session may stay open.
	pairingTTL = 3 * time.Minute
	// maxInflightPairings caps concurrent pending sessions.
	maxInflightPairings = 3
	// firstCodeTimeout is how long Start waits for the first QR code.
	firstCodeTimeout = 10 * time.Second
	// pairSettleTimeout is how long to wait, after a successful scan, for the
	// post-pairing reconnect before disconnecting the temporary client.
	pairSettleTimeout = 15 * time.Second
	// finishedRetention keeps finished sessions around so pollers see the result.
	finishedRetention = 5 * time.Minute
	// recentPairingTTL protects freshly paired devices whose channel isn't saved yet.
	recentPairingTTL = 30 * time.Minute
)

// ErrTooManyPairings is returned by Start when maxInflightPairings are pending.
var ErrTooManyPairings = errors.New("too many pairing sessions in progress")

// PairingResult is the externally visible state of a pairing session.
type PairingResult struct {
	Status    PairingStatus
	QRCode    string // current code while pending
	DeviceJID string // on success
	Error     string
}

// pairClient is the part of *whatsmeow.Client that pairing uses.
type pairClient interface {
	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
	AddEventHandler(handler whatsmeow.EventHandler) uint32
	Connect() error
	Disconnect()
	// DeviceJID returns the paired device JID, or "" when not paired.
	DeviceJID() string
	// DeleteDevice removes the device's keys, if any were stored.
	DeleteDevice(ctx context.Context) error
}

type pairing struct {
	mu         sync.Mutex
	result     PairingResult
	finishedAt time.Time
}

func (p *pairing) get() PairingResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

func (p *pairing) update(fn func(r *PairingResult)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.result)
	if p.result.Status != PairingPending && p.finishedAt.IsZero() {
		p.finishedAt = time.Now()
	}
}

// PairingManager runs QR pairing sessions that create new WhatsApp devices
// in the shared store, before any channel exists ("bind before create").
// State is in memory: this assumes a single WeKnora instance.
type PairingManager struct {
	newClient func(ctx context.Context) (pairClient, error)
	ttl       time.Duration
	settle    time.Duration

	mu       sync.Mutex
	pairings map[string]*pairing
	recent   map[string]time.Time // device JID → paired at
}

// NewPairingManager returns a manager that creates devices in store.
func NewPairingManager(s *Store) *PairingManager {
	m := newPairingManager(func(ctx context.Context) (pairClient, error) {
		c, err := s.Container(ctx)
		if err != nil {
			return nil, err
		}
		device := c.NewDevice()
		return &whatsmeowPairClient{Client: whatsmeow.NewClient(device, newWALogger("pair")), device: device}, nil
	})
	return m
}

func newPairingManager(newClient func(ctx context.Context) (pairClient, error)) *PairingManager {
	return &PairingManager{
		newClient: newClient,
		ttl:       pairingTTL,
		settle:    pairSettleTimeout,
		pairings:  make(map[string]*pairing),
		recent:    make(map[string]time.Time),
	}
}

// Start opens a new pairing session and returns its ID with the first QR
// code. The session outlives ctx; it ends on success, timeout or TTL expiry.
func (m *PairingManager) Start(ctx context.Context) (string, PairingResult, error) {
	m.mu.Lock()
	m.pruneLocked()
	pending := 0
	for _, p := range m.pairings {
		if p.get().Status == PairingPending {
			pending++
		}
	}
	m.mu.Unlock()
	if pending >= maxInflightPairings {
		return "", PairingResult{}, ErrTooManyPairings
	}

	client, err := m.newClient(ctx)
	if err != nil {
		return "", PairingResult{}, fmt.Errorf("create whatsapp client: %w", err)
	}

	// The session must not die with the HTTP request.
	runCtx, cancel := context.WithTimeout(context.Background(), m.ttl)
	qrCh, err := client.GetQRChannel(runCtx)
	if err != nil {
		cancel()
		return "", PairingResult{}, fmt.Errorf("get qr channel: %w", err)
	}
	connected := make(chan struct{}, 1)
	client.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	if err := client.Connect(); err != nil {
		cancel()
		client.Disconnect()
		return "", PairingResult{}, fmt.Errorf("connect: %w", err)
	}

	p := &pairing{result: PairingResult{Status: PairingPending}}
	id := uuid.New().String()

	// Wait for the first code so the UI can show a QR immediately.
	timer := time.NewTimer(firstCodeTimeout)
	defer timer.Stop()
	select {
	case item, ok := <-qrCh:
		if !ok {
			p.update(func(r *PairingResult) { r.Status = PairingTimeout })
		} else {
			m.handleItem(runCtx, client, p, item, connected)
		}
	case <-timer.C:
		p.update(func(r *PairingResult) {
			r.Status = PairingError
			r.Error = "no QR code received"
		})
	case <-ctx.Done():
		p.update(func(r *PairingResult) {
			r.Status = PairingError
			r.Error = ctx.Err().Error()
		})
	}

	m.mu.Lock()
	m.pairings[id] = p
	m.mu.Unlock()

	if p.get().Status == PairingPending {
		go m.run(runCtx, cancel, client, p, qrCh, connected)
	} else {
		m.finish(runCtx, cancel, client, p)
	}
	return id, p.get(), nil
}

// Status returns the current state of a pairing session.
func (m *PairingManager) Status(id string) (PairingResult, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	p, ok := m.pairings[id]
	if !ok {
		return PairingResult{}, false
	}
	return p.get(), true
}

// RecentDeviceJIDs lists devices paired in the last recentPairingTTL; their
// channel may not be saved yet, so device cleanup must keep them.
func (m *PairingManager) RecentDeviceJIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	out := make([]string, 0, len(m.recent))
	for jid := range m.recent {
		out = append(out, jid)
	}
	return out
}

func (m *PairingManager) pruneLocked() {
	now := time.Now()
	for id, p := range m.pairings {
		p.mu.Lock()
		expired := !p.finishedAt.IsZero() && now.Sub(p.finishedAt) > finishedRetention
		p.mu.Unlock()
		if expired {
			delete(m.pairings, id)
		}
	}
	for jid, at := range m.recent {
		if now.Sub(at) > recentPairingTTL {
			delete(m.recent, jid)
		}
	}
}

// run drains the QR channel until the session ends.
func (m *PairingManager) run(ctx context.Context, cancel context.CancelFunc, client pairClient,
	p *pairing, qrCh <-chan whatsmeow.QRChannelItem, connected <-chan struct{},
) {
	for item := range qrCh {
		m.handleItem(ctx, client, p, item, connected)
		if p.get().Status != PairingPending {
			break
		}
	}
	// whatsmeow closes the channel without a final item when ctx (the TTL) ends.
	p.update(func(r *PairingResult) {
		if r.Status == PairingPending {
			r.Status = PairingTimeout
		}
	})
	m.finish(ctx, cancel, client, p)
}

func (m *PairingManager) handleItem(ctx context.Context, client pairClient, p *pairing,
	item whatsmeow.QRChannelItem, connected <-chan struct{},
) {
	switch item.Event {
	case whatsmeow.QRChannelEventCode:
		p.update(func(r *PairingResult) { r.QRCode = item.Code })
	case whatsmeow.QRChannelSuccess.Event:
		// whatsmeow has saved the keys; the server now drops the socket and the
		// client reconnects. Let that settle before disconnecting.
		select {
		case <-connected:
		case <-time.After(m.settle):
		case <-ctx.Done():
		}
		jid := client.DeviceJID()
		if jid == "" {
			p.update(func(r *PairingResult) {
				r.Status = PairingError
				r.Error = "paired but no device id"
			})
			return
		}
		m.mu.Lock()
		m.recent[jid] = time.Now()
		m.mu.Unlock()
		p.update(func(r *PairingResult) {
			r.Status = PairingSuccess
			r.QRCode = ""
			r.DeviceJID = jid
		})
		logger.Infof(ctx, "[WhatsApp] Paired device %s", jid)
	case whatsmeow.QRChannelTimeout.Event:
		p.update(func(r *PairingResult) { r.Status = PairingTimeout })
	case whatsmeow.QRChannelEventPasskeyRequest, whatsmeow.QRChannelEventPasskeyResponse:
		// Not used by QR login.
	default:
		msg := item.Event
		if item.Error != nil {
			msg = item.Error.Error()
		}
		p.update(func(r *PairingResult) {
			r.Status = PairingError
			r.Error = msg
		})
	}
}

// finish disconnects the temporary client and, unless pairing succeeded,
// deletes any device keys it may have stored.
func (m *PairingManager) finish(ctx context.Context, cancel context.CancelFunc, client pairClient, p *pairing) {
	defer cancel()
	client.Disconnect()
	res := p.get()
	if res.Status == PairingSuccess {
		return
	}
	if err := client.DeleteDevice(context.WithoutCancel(ctx)); err != nil {
		logger.Warnf(ctx, "[WhatsApp] Delete unpaired device: %v", err)
	}
	if res.Status == PairingError {
		logger.Warnf(ctx, "[WhatsApp] Pairing failed: %s", res.Error)
	}
}

// whatsmeowPairClient adapts *whatsmeow.Client to pairClient.
type whatsmeowPairClient struct {
	*whatsmeow.Client
	device *store.Device
}

func (c *whatsmeowPairClient) DeviceJID() string {
	if c.device.ID == nil {
		return ""
	}
	return c.device.ID.String()
}

func (c *whatsmeowPairClient) DeleteDevice(ctx context.Context) error {
	if c.device.ID == nil {
		return nil // never paired, nothing stored
	}
	return c.device.Delete(ctx)
}
