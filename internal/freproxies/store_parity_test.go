package freproxies

import (
	"context"
	"testing"
)

// bothStores runs fn against every Store implementation. Any behaviour the
// service layer relies on has to hold for both, otherwise a Redis outage
// silently changes what the API returns.
func bothStores(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, NewMemoryStore()) })
	t.Run("redis", func(t *testing.T) {
		s, _ := newFakeRedisStore(t)
		fn(t, s)
	})
}

// RandomN on a raw-only pool must refuse on both backends. Serving untested
// addresses is how the public get/7892 endpoints filled with dead proxies.
func TestRandomNParityOnUnvalidatedPool(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		if _, err := s.AddRaw(ctx, []Proxy{
			{Host: "10.0.0.1", Port: 8080, Protocol: "http"},
			{Host: "10.0.0.2", Port: 8081, Protocol: "socks5"},
		}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}

		_, err := s.RandomN(ctx, "", 2)
		if err == nil {
			t.Fatal("raw-only pool must not be served as available; validate first")
		}
	})
}

// The protocol filter must not be silently ignored on the raw fallback path.
func TestRandomNParityRespectsProtocolOnRawPool(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		if _, err := s.AddRaw(ctx, []Proxy{
			{Host: "10.0.0.1", Port: 8080, Protocol: "http"},
			{Host: "10.0.0.2", Port: 8081, Protocol: "socks5"},
		}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}

		_, err := s.RandomN(ctx, "socks5", 4)
		if err == nil {
			t.Fatal("unvalidated socks5 raw must not be served")
		}
	})
}

// A pool whose proxies have all failed validation must report empty rather than
// keep handing out addresses that are known to be dead.
func TestRandomNParityOnExhaustedPool(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		addr := normalizeAddr("10.0.0.1", 8080)
		if _, err := s.AddRaw(ctx, []Proxy{{Host: "10.0.0.1", Port: 8080, Protocol: "http"}}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, addr, 0, false); err != nil {
			t.Fatalf("MarkValidated: %v", err)
		}
		if _, err := s.RandomN(ctx, "", 1); err == nil {
			t.Error("expected an error once every proxy has been evicted")
		}
	})
}

// Validated proxies must be preferred over raw ones: the raw pool is only a
// fallback for a pool that has not been validated yet.
func TestRandomNParityPrefersValidated(t *testing.T) {
	ctx := context.Background()
	bothStores(t, func(t *testing.T, s Store) {
		good := normalizeAddr("10.0.0.1", 8080)
		if _, err := s.AddRaw(ctx, []Proxy{
			{Host: "10.0.0.1", Port: 8080, Protocol: "http"},
			{Host: "10.0.0.2", Port: 8081, Protocol: "http"},
			{Host: "10.0.0.3", Port: 8082, Protocol: "http"},
		}); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		if err := s.MarkValidated(ctx, good, 15, true); err != nil {
			t.Fatalf("MarkValidated: %v", err)
		}

		got, err := s.RandomN(ctx, "", 1)
		if err != nil {
			t.Fatalf("RandomN: %v", err)
		}
		if len(got) != 1 || got[0].Addr != good {
			t.Errorf("expected the validated proxy %s, got %+v", good, got)
		}
	})
}
