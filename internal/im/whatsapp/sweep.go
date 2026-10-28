package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	// sweepInterval is how often orphaned devices are cleaned up.
	sweepInterval = time.Hour
	// sweepTimeout bounds one sweep, including best-effort logouts.
	sweepTimeout = 5 * time.Minute
	// logoutConnectTimeout bounds the login a logout needs first.
	logoutConnectTimeout = 20 * time.Second
)

// sweep logs out and deletes devices that no live WhatsApp channel
// references, except those in keep (paired but not saved to a channel yet).
// Core has no delete hook for adapters, so this is how deleted channels,
// rebinds (the old device) and abandoned pairings get unlinked.
func (s *Store) sweep(ctx context.Context, keep []string) error {
	if s.db == nil {
		return nil
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	referenced, err := referencedDevices(ctx, s)
	if err != nil {
		return err
	}
	for _, jid := range keep {
		referenced[jid] = true
	}

	c, err := s.Container(ctx)
	if err != nil {
		return err
	}
	devices, err := c.GetAllDevices(ctx)
	if err != nil {
		return fmt.Errorf("list whatsapp devices: %w", err)
	}
	for _, d := range devices {
		if d.ID == nil || referenced[d.ID.String()] {
			continue
		}
		jid := d.ID.String()
		logger.Infof(ctx, "[WhatsApp] Removing orphaned device %s", jid)
		if err := s.logout(ctx, d); err != nil {
			logger.Warnf(ctx, "[WhatsApp] Remove orphaned device %s: %v", jid, err)
		}
	}
	return nil
}

// referencedDevices returns the device JIDs of WhatsApp channels that are
// not soft-deleted. Credentials are parsed in Go to stay portable across
// Postgres and SQLite.
func referencedDevices(ctx context.Context, s *Store) (map[string]bool, error) {
	var creds []types.JSON
	if err := s.db.WithContext(ctx).Model(&im.IMChannel{}).
		Where("platform = ?", string(im.PlatformWhatsApp)).
		Pluck("credentials", &creds).Error; err != nil {
		return nil, fmt.Errorf("list whatsapp channels: %w", err)
	}
	out := make(map[string]bool, len(creds))
	for _, raw := range creds {
		c, err := ParseCredentials(raw)
		if err != nil || c.DeviceJID == "" {
			continue
		}
		out[c.DeviceJID] = true
	}
	return out, nil
}

// startSweeping runs a sweep every sweepInterval for the life of the process.
// Safe to call repeatedly; only the first call starts the loop.
func (s *Store) startSweeping() {
	if s.db == nil {
		return
	}
	s.sweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(sweepInterval)
			defer ticker.Stop()
			for range ticker.C {
				s.sweepNow()
			}
		}()
	})
}

// sweepNow runs one sweep with its own timeout, logging any failure.
func (s *Store) sweepNow() {
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()
	if err := s.sweep(ctx, s.RecentlyPaired()); err != nil {
		logger.Warnf(ctx, "[WhatsApp] Device sweep: %v", err)
	}
}

// logoutDevice unlinks a device from the phone (best effort) and deletes its
// keys. Logging out needs a live, logged-in connection; if that can't be
// had, the keys are deleted anyway.
func logoutDevice(ctx context.Context, device *store.Device) error {
	client := whatsmeow.NewClient(device, newWALogger("sweep"))
	ready := make(chan struct{}, 1)
	client.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.Connected, *events.LoggedOut:
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})

	var logoutErr error
	if err := client.Connect(); err != nil {
		logoutErr = err
	} else {
		select {
		case <-ready:
			logoutErr = client.Logout(ctx) // deletes the keys on success
		case <-time.After(logoutConnectTimeout):
			logoutErr = errors.New("timed out waiting for login")
		case <-ctx.Done():
			logoutErr = ctx.Err()
		}
		client.Disconnect()
	}
	if logoutErr == nil {
		return nil
	}
	if err := device.Delete(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("logout: %v; delete: %w", logoutErr, err)
	}
	return nil
}
