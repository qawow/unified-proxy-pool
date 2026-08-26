package freproxies

import (
	"context"
	"testing"
	"time"

	"unified-proxy-pool/internal/crawlers"
)

// The hot cache falls back to its unfiltered snapshot when a protocol matches
// nothing in it (hotcache.go, Pick). Before protocol was enforced downstream,
// that fallback short-circuited the whole ladder: a socks5 request was answered
// with an http proxy from the cache while real socks5 proxies sat in the store,
// never queried, and no error was reported.
func TestPickPrefersRealProtocolOverStaleHotCache(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)

	// Store holds a genuine socks5 proxy.
	socks := Proxy{Host: "10.0.0.9", Port: 1080, Addr: "10.0.0.9:1080", Protocol: "socks5"}
	if _, err := svc.store.AddRaw(ctx, []Proxy{socks}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	if err := svc.store.MarkValidated(ctx, socks.Addr, 100, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}

	// Hot cache holds only http, as it would right after an http-only refresh.
	svc.hot = NewHotCache(svc.store, 64, time.Second)
	svc.hot.items = []Proxy{
		{Host: "10.0.0.1", Port: 8080, Addr: "10.0.0.1:8080", Protocol: "http", Score: 100, Validated: true},
	}

	res, err := svc.Pick(ctx, PickOptions{Protocol: "socks5", N: 1})
	if err != nil {
		t.Fatalf("Pick(socks5): %v", err)
	}
	if got := res.Items[0].Protocol; got != "socks5" {
		t.Errorf("asked for socks5, got %s (%s) — the hot cache fallback outranked a real socks5 proxy",
			got, res.Items[0].Addr)
	}
}

// filterCandidates is the choke point that enforces protocol; check it directly
// so the guarantee does not depend on which rung produced the candidates.
func TestFilterCandidatesEnforcesProtocol(t *testing.T) {
	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	items := []Proxy{
		{Addr: "1.1.1.1:80", Protocol: "http"},
		{Addr: "2.2.2.2:1080", Protocol: "socks5"},
		{Addr: "3.3.3.3:1080", Protocol: "SOCKS5"}, // case must not matter
	}
	got := svc.filterCandidates(items, PickOptions{Protocol: "socks5"}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want the 2 socks5 ones: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Addr == "1.1.1.1:80" {
			t.Error("an http proxy survived a socks5 filter")
		}
	}
	// An empty protocol means the caller does not care.
	if got := svc.filterCandidates(items, PickOptions{}, nil); len(got) != 3 {
		t.Errorf("empty protocol filtered %d of 3 candidates", 3-len(got))
	}
}

// The documented ladder still relaxes protocol when the pool genuinely has no
// match — enforcement must not turn a servable request into a 404.
func TestPickStillRelaxesProtocolWhenPoolHasNone(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t, proxyAt("10.0.0.1", 8080, 50, 100)) // http only

	res, err := svc.Pick(ctx, PickOptions{Protocol: "socks5", N: 1})
	if err != nil {
		t.Fatalf("expected the ladder to relax protocol on an http-only pool, got %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("got %d proxies, want 1", len(res.Items))
	}
	if res.Items[0].Protocol != "http" {
		t.Errorf("got protocol %s, want the relaxed http proxy", res.Items[0].Protocol)
	}
}
