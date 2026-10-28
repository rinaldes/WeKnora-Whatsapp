package whatsapp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// newSweepStore returns a store on SQLite with a minimal im_channels table
// (the production schema's uuid_generate_v4() default doesn't parse on SQLite)
// and a logout that just deletes, recording what it removed.
func newSweepStore(t *testing.T) (*Store, *[]string) {
	t.Helper()
	s := newTestStore(t)
	if err := s.db.Exec(`CREATE TABLE im_channels (
		id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, agent_id TEXT NOT NULL,
		platform TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', enabled NUMERIC NOT NULL DEFAULT 1,
		mode TEXT NOT NULL DEFAULT 'websocket', output_mode TEXT NOT NULL DEFAULT 'stream',
		locale TEXT NOT NULL DEFAULT '', knowledge_base_id TEXT DEFAULT '',
		bot_identity TEXT NOT NULL DEFAULT '', session_mode TEXT NOT NULL DEFAULT 'user',
		credentials TEXT NOT NULL DEFAULT '{}', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create im_channels: %v", err)
	}
	var mu sync.Mutex
	var removed []string
	s.logout = func(ctx context.Context, d *store.Device) error {
		mu.Lock()
		removed = append(removed, d.ID.String())
		mu.Unlock()
		return d.Delete(ctx)
	}
	return s, &removed
}

func addDevice(t *testing.T, s *Store, user string) string {
	t.Helper()
	ctx := context.Background()
	c, err := s.Container(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := c.NewDevice()
	jid := types.NewADJID(user, 0, 3)
	d.ID = &jid
	d.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := c.PutDevice(ctx, d); err != nil {
		t.Fatalf("put device: %v", err)
	}
	return jid.String()
}

func addChannel(t *testing.T, s *Store, id, platform, jid string, deleted bool) {
	t.Helper()
	deletedAt := "NULL"
	if deleted {
		deletedAt = "CURRENT_TIMESTAMP"
	}
	if err := s.db.Exec(fmt.Sprintf(`INSERT INTO im_channels (id, tenant_id, agent_id, platform, credentials, deleted_at)
		VALUES (?, 1, 'a', ?, ?, %s)`, deletedAt), id, platform,
		fmt.Sprintf(`{"device_jid":%q,"owner_number":"1"}`, jid)).Error; err != nil {
		t.Fatalf("insert channel: %v", err)
	}
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	s, removed := newSweepStore(t)

	live := addDevice(t, s, "111")
	disabled := addDevice(t, s, "222")
	recent := addDevice(t, s, "333")
	orphan := addDevice(t, s, "444")
	deletedChannel := addDevice(t, s, "555")

	addChannel(t, s, "c1", "whatsapp", live, false)
	if err := s.db.Exec(`INSERT INTO im_channels (id, tenant_id, agent_id, platform, enabled, credentials)
		VALUES ('c2', 1, 'a', 'whatsapp', 0, ?)`, fmt.Sprintf(`{"device_jid":%q}`, disabled)).Error; err != nil {
		t.Fatal(err)
	}
	addChannel(t, s, "c3", "whatsapp", deletedChannel, true)
	addChannel(t, s, "c4", "telegram", orphan, false) // other platforms don't count

	if err := Sweep(ctx, s, []string{recent}); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	got := append([]string(nil), *removed...)
	sort.Strings(got)
	want := []string{orphan, deletedChannel}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("removed %v, want %v", got, want)
	}

	c, _ := s.Container(ctx)
	devices, err := c.GetAllDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, d := range devices {
		left = append(left, d.ID.String())
	}
	sort.Strings(left)
	wantLeft := []string{live, disabled, recent}
	sort.Strings(wantLeft)
	if fmt.Sprint(left) != fmt.Sprint(wantLeft) {
		t.Fatalf("devices left %v, want %v", left, wantLeft)
	}
}

func TestSweepWithoutDatabaseIsNoop(t *testing.T) {
	if err := Sweep(context.Background(), NewStore(nil), nil); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
}

func TestRecentlyPaired(t *testing.T) {
	s := NewStore(nil)
	s.rememberPaired("a@s.whatsapp.net")
	if got := s.RecentlyPaired(); len(got) != 1 {
		t.Fatalf("RecentlyPaired() = %v", got)
	}
}
