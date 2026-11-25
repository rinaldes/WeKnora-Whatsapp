// Package whatsapp implements the WhatsApp IM channel on top of whatsmeow
// (WhatsApp multi-device). Device keys live in WeKnora's own database in
// whatsmeow's whatsmeow_* tables; the channel only stores the device JID.
package whatsapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"gorm.io/gorm"
)

// Store lazily opens whatsmeow's device store on WeKnora's own database.
// whatsmeow owns and migrates its tables; nothing is added to migrations/.
type Store struct {
	db      *gorm.DB
	once    sync.Once
	c       *sqlstore.Container
	initErr error

	// recent holds devices paired in the last recentPairingTTL. Their channel
	// may not be saved yet, so the device sweep must keep them.
	recentMu sync.Mutex
	recent   map[string]time.Time

	sweepMu   sync.Mutex // one sweep at a time
	sweepOnce sync.Once
	// logout unlinks and deletes an orphaned device; replaced in tests.
	logout func(ctx context.Context, device *store.Device) error
}

// NewStore returns a Store backed by db. No connection or migration happens
// until Container is first called.
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db, recent: make(map[string]time.Time), logout: logoutDevice}
}

// Container returns the shared device container, creating it and running
// whatsmeow's schema upgrade on first use.
func (s *Store) Container(ctx context.Context) (*sqlstore.Container, error) {
	s.once.Do(func() {
		s.c, s.initErr = s.open(ctx)
	})
	return s.c, s.initErr
}

func (s *Store) open(ctx context.Context) (*sqlstore.Container, error) {
	if s.db == nil {
		return nil, fmt.Errorf("whatsapp store: no database")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return nil, fmt.Errorf("whatsapp store: get sql.DB: %w", err)
	}
	dialect, err := sqlDialect(s.db.Dialector.Name())
	if err != nil {
		return nil, err
	}
	c := sqlstore.NewWithDB(sqlDB, dialect, newWALogger("store"))
	if err := c.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("whatsapp store: upgrade: %w", err)
	}
	return c, nil
}

// rememberPaired records a freshly paired device.
func (s *Store) rememberPaired(jid string) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	s.recent[jid] = time.Now()
}

// RecentlyPaired lists devices paired in the last recentPairingTTL.
func (s *Store) RecentlyPaired() []string {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	out := make([]string, 0, len(s.recent))
	for jid, at := range s.recent {
		if time.Since(at) > recentPairingTTL {
			delete(s.recent, jid)
			continue
		}
		out = append(out, jid)
	}
	return out
}

// sqlDialect maps a gorm dialector name to whatsmeow's dialect name.
func sqlDialect(gormName string) (string, error) {
	switch gormName {
	case "postgres":
		return "postgres", nil
	case "sqlite":
		return "sqlite3", nil
	default:
		return "", fmt.Errorf("whatsapp store: unsupported database %q", gormName)
	}
}
