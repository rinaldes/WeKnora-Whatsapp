package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "wa.db") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(newTestDB(t))
}

func TestStoreContainerSQLite(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	c1, err := s.Container(ctx)
	if err != nil {
		t.Fatalf("Container() error = %v", err)
	}
	c2, err := s.Container(ctx)
	if err != nil || c2 != c1 {
		t.Fatalf("second Container() = %p, %v; want same container %p", c2, err, c1)
	}

	if !s.db.Migrator().HasTable("whatsmeow_device") {
		t.Fatal("whatsmeow_device table not created")
	}
	devices, err := c1.GetAllDevices(ctx)
	if err != nil {
		t.Fatalf("GetAllDevices() error = %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("GetAllDevices() = %d devices, want 0", len(devices))
	}
}

func TestStoreUpgradeIsRepeatable(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := NewStore(db).Container(ctx); err != nil {
		t.Fatalf("first store: %v", err)
	}
	// A restart builds a fresh Store on the same database.
	if _, err := NewStore(db).Container(ctx); err != nil {
		t.Fatalf("second store: %v", err)
	}
}

func TestSQLDialect(t *testing.T) {
	for in, want := range map[string]string{"postgres": "postgres", "sqlite": "sqlite3"} {
		if got, err := sqlDialect(in); err != nil || got != want {
			t.Errorf("sqlDialect(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := sqlDialect("mysql"); err == nil {
		t.Error("sqlDialect(mysql) should fail")
	}
}

// TestStoreContainerPostgres runs only when WHATSAPP_TEST_POSTGRES_DSN points
// at a scratch database, e.g. "host=localhost user=postgres password=x dbname=wa".
func TestStoreContainerPostgres(t *testing.T) {
	dsn := os.Getenv("WHATSAPP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WHATSAPP_TEST_POSTGRES_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ { // second pass simulates a restart
		c, err := NewStore(db).Container(ctx)
		if err != nil {
			t.Fatalf("Container() pass %d error = %v", i, err)
		}
		if _, err := c.GetAllDevices(ctx); err != nil {
			t.Fatalf("GetAllDevices() error = %v", err)
		}
	}
}
