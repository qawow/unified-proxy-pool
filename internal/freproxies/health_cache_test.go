package freproxies

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// listValidatedStore counts ListValidated calls so the health cache can be
// proven to cut scans rather than just assumed to. (There is already a
// countingStore for List; this one wraps the validated-set reader.)
type listValidatedStore struct {
	Store
	lists atomic.Int64
}

func (c *listValidatedStore) ListValidated(ctx context.Context, limit int64) ([]Proxy, error) {
	c.lists.Add(1)
	return c.Store.ListValidated(ctx, limit)
}

// TestHealthCacheAvoidsRepeatedScans: the panel polls both /api/pool/health
// and the overview (which embeds the verdict) on a short timer. Without the
// cache every poll scans the whole validated set; with it, one scan per window.
func TestHealthCacheAvoidsRepeatedScans(t *testing.T) {
	base := NewMemoryStore()
	store := &listValidatedStore{Store: base}
	ctx := context.Background()
	_, _ = store.AddRaw(ctx, []Proxy{{Host: "10.0.0.9", Port: 8080, Protocol: "http", Source: "t"}})
	_ = store.MarkValidated(ctx, "10.0.0.9:8080", 300, true)

	s := NewService(store, nil, nil, false)
	for i := 0; i < 5; i++ {
		h := s.Health(ctx)
		if h.Available != 1 {
			t.Fatalf("call %d: Available=%d want 1", i, h.Available)
		}
	}
	// First call scans; the next four must all be served from cache.
	if got := store.lists.Load(); got != 1 {
		t.Fatalf("ListValidated called %d times for 5 Health calls, want 1", got)
	}

	// Past the window, a fresh scan is allowed — the verdict must not freeze.
	time.Sleep(healthTTL + 20*time.Millisecond)
	_ = s.Health(ctx)
	if got := store.lists.Load(); got != 2 {
		t.Fatalf("after TTL, ListValidated called %d times, want 2", got)
	}
}

// TestHealthCacheServesStaleWithinTTL: a cached verdict is returned verbatim
// even if the store changed underneath; coherence within the window beats
// freshness for a number that moves per batch.
func TestHealthCacheServesStaleWithinTTL(t *testing.T) {
	base := NewMemoryStore()
	store := &listValidatedStore{Store: base}
	ctx := context.Background()
	_, _ = store.AddRaw(ctx, []Proxy{{Host: "10.0.0.9", Port: 8080, Protocol: "http", Source: "t"}})
	_ = store.MarkValidated(ctx, "10.0.0.9:8080", 300, true)

	s := NewService(store, nil, nil, false)
	first := s.Health(ctx)
	// One live proxy is below the healthy threshold of usable proxies, so the
	// verdict is critical — the point is that the cached value persists, not
	// which verdict it happens to be.
	if first.Status != "critical" {
		t.Fatalf("first: %s, want critical", first.Status)
	}

	// Pool degrades to slow; within the TTL the panel still sees the cached
	// verdict, so the UI stays coherent for the duration of a poll cycle.
	_, _ = store.AddRaw(ctx, []Proxy{{Host: "10.0.0.8", Port: 8080, Protocol: "http", Source: "t"}})
	_ = store.MarkValidated(ctx, "10.0.0.8:8080", 9000, true)
	cached := s.Health(ctx)
	if cached.Status != first.Status {
		t.Fatalf("within TTL: %s, want cached %s", cached.Status, first.Status)
	}
}
