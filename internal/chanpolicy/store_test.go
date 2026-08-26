package chanpolicy

import (
	"path/filepath"
	"testing"
	"time"

	"unified-proxy-pool/internal/db"
)

// newTestSQLStore opens a migrated throwaway database wired to the same fake
// clock the Registry uses, so store and registry agree on what has expired.
func newTestSQLStore(t *testing.T, clk *fakeClock) *SQLStore {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	s := NewSQLStore(store.DB)
	s.SetClock(clk.now)
	return s
}

// A restart must not hand a known-bad proxy straight back to the channel that
// just banned it.
func TestBansSurviveRestart(t *testing.T) {
	clk := newClock()
	sqlStore := newTestSQLStore(t, clk)
	p := Defaults()
	p.BanTTLSec = 3600

	first := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	banNow(t, first, "taobao.com", "1.2.3.4:8080")

	loaded, err := sqlStore.LoadBans()
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d bans, want 1", len(loaded))
	}

	// Fresh registry, same database — the ban has to come back.
	second := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	if n := second.Restore(loaded); n != 1 {
		t.Fatalf("Restore reinstated %d bans, want 1", n)
	}
	if !second.Banned("taobao.com", "1.2.3.4:8080") {
		t.Error("ban did not survive the restart")
	}
	if second.Banned("amazon.com", "1.2.3.4:8080") {
		t.Error("restored ban leaked to another channel")
	}
}

// A long outage should release everything rather than resuming stale bans.
func TestExpiredBansAreNotRestored(t *testing.T) {
	clk := newClock()
	sqlStore := newTestSQLStore(t, clk)
	p := Defaults()
	p.BanTTLSec = 60

	first := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	banNow(t, first, "ch", "1.1.1.1:80")

	loaded, err := sqlStore.LoadBans()
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}

	clk.advance(2 * time.Hour)
	second := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	if n := second.Restore(loaded); n != 0 {
		t.Errorf("Restore reinstated %d expired bans, want 0", n)
	}
	if second.Banned("ch", "1.1.1.1:80") {
		t.Error("an expired ban was restored")
	}
}

func TestUnbanRemovesPersistedRow(t *testing.T) {
	clk := newClock()
	sqlStore := newTestSQLStore(t, clk)
	p := Defaults()
	p.BanTTLSec = 3600

	r := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	banNow(t, r, "ch", "1.1.1.1:80")
	if !r.Unban("ch", "1.1.1.1:80") {
		t.Fatal("Unban found nothing to clear")
	}
	loaded, err := sqlStore.LoadBans()
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("%d rows survived Unban, want 0", len(loaded))
	}
}

func TestResetChannelRemovesPersistedRows(t *testing.T) {
	clk := newClock()
	sqlStore := newTestSQLStore(t, clk)
	p := Defaults()
	p.BanTTLSec = 3600

	r := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	banNow(t, r, "ch", "1.1.1.1:80")
	banNow(t, r, "ch", "2.2.2.2:80")
	banNow(t, r, "other.com", "3.3.3.3:80")
	r.ResetChannel("ch")

	loaded, err := sqlStore.LoadBans()
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Channel != "other.com" {
		t.Errorf("after reset got %+v, want only the other.com ban", loaded)
	}
}

// Re-banning the same pair must update the row rather than fail on the primary key.
func TestRepeatBanUpsertsRow(t *testing.T) {
	clk := newClock()
	sqlStore := newTestSQLStore(t, clk)
	p := Defaults()
	p.BanTTLSec = 60

	r := New(Options{Policy: p, Now: clk.now, Persist: sqlStore})
	banNow(t, r, "ch", "1.1.1.1:80")
	clk.advance(70 * time.Second)
	banNow(t, r, "ch", "1.1.1.1:80")

	loaded, err := sqlStore.LoadBans()
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d rows, want 1 upserted row", len(loaded))
	}
	if loaded[0].Strikes != 2 {
		t.Errorf("persisted strikes = %d, want 2", loaded[0].Strikes)
	}
}

func TestNilPersisterIsSafe(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	banNow(t, r, "ch", "1.1.1.1:80")
	r.Unban("ch", "1.1.1.1:80")
	r.ResetChannel("ch")
	r.DeleteChannel("ch")
	r.Sweep()
}
