package freproxies

import (
	"context"
	"path/filepath"
	"testing"

	"unified-proxy-pool/internal/db"
)

func TestMemoryStoreSQLiteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	ms := PersistMemoryStore(NewMemoryStore(), store)
	if _, err := ms.AddRaw(ctx, []Proxy{
		{Host: "1.2.3.4", Port: 8080, Protocol: "http", Source: "t"},
		{Host: "5.6.7.8", Port: 1080, Protocol: "socks5", Source: "t"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	if err := ms.MarkValidated(ctx, "1.2.3.4:8080", 120, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}
	if err := ms.SetScraperEnabled(ctx, "thespeedx-http", false); err != nil {
		t.Fatalf("toggle: %v", err)
	}

	if err := ms.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again := PersistMemoryStore(NewMemoryStore(), store)
	total, validated, raw, err := again.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 2 || validated != 1 || raw != 1 {
		t.Fatalf("got total=%d validated=%d raw=%d", total, validated, raw)
	}
	on, err := again.IsScraperEnabled(ctx, "thespeedx-http", true)
	if err != nil {
		t.Fatalf("IsScraperEnabled: %v", err)
	}
	if on {
		t.Fatal("expected scraper stay disabled after reload")
	}
	p, err := again.Get(ctx, "1.2.3.4:8080")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.LatencyMS != 120 || !p.Validated {
		t.Fatalf("proxy not restored: %+v", p)
	}
}
