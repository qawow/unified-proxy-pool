package freproxies

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// newHotCacheWith builds a hot cache over a memory store holding the given
// proxies as validated, so PickDistinct exercises the same data path as
// production.
func newHotCacheWith(t *testing.T, proxies ...Proxy) *HotCache {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.AddRaw(ctx, proxies); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	for _, p := range proxies {
		addr := p.Addr
		if addr == "" {
			addr = normalizeAddr(p.Host, p.Port)
		}
		if err := store.MarkValidated(ctx, addr, 10, true); err != nil {
			t.Fatalf("MarkValidated(%s): %v", addr, err)
		}
	}
	h := NewHotCache(store, 64, time.Minute)
	h.refresh(ctx)
	return h
}

func hotProxy(host string, port int, proto, region string) Proxy {
	return Proxy{
		Host: host, Port: port, Addr: fmt.Sprintf("%s:%d", host, port),
		Protocol: proto, Region: region, Validated: true,
	}
}

// Chain members must not share a host: two ports on one machine are one
// failure domain, not two hops.
func TestPickDistinctUniqueHosts(t *testing.T) {
	h := newHotCacheWith(t,
		hotProxy("10.1.1.1", 8080, "http", "US"),
		hotProxy("10.1.1.1", 8081, "http", "US"), // same host, another port
		hotProxy("10.2.2.2", 8080, "http", "JP"),
	)
	for i := 0; i < 20; i++ {
		out := h.PickDistinct(3, false, "", "", "", "")
		seen := map[string]bool{}
		for _, p := range out {
			if seen[p.Host] {
				t.Fatalf("host %s picked twice in %+v", p.Host, out)
			}
			seen[p.Host] = true
		}
		if len(out) != 2 {
			t.Fatalf("picked %d proxies, want the 2 distinct hosts", len(out))
		}
	}
}

// The entry slot takes only entryProto-compatible proxies when such a proxy
// exists in the window.
func TestPickDistinctEntryProto(t *testing.T) {
	h := newHotCacheWith(t,
		hotProxy("10.1.1.1", 8080, "http", "US"),
		hotProxy("10.2.2.2", 1080, "socks5", "JP"),
	)
	for i := 0; i < 20; i++ {
		out := h.PickDistinct(2, false, "socks5", "", "", "")
		if len(out) == 0 || out[0].Protocol != "socks5" {
			t.Fatalf("entry hop = %+v, want the socks5 proxy first", out)
		}
	}
}

// Symmetrically, the last slot of the window honours exitProto. Two socks5
// proxies make the outcome order-independent: wherever the scan places them,
// the exit slot can only be filled by one of them.
func TestPickDistinctExitProto(t *testing.T) {
	h := newHotCacheWith(t,
		hotProxy("10.1.1.1", 8080, "http", "US"),
		hotProxy("10.2.2.2", 1080, "socks5", "DE"),
		hotProxy("10.3.3.3", 1080, "socks5", "JP"),
	)
	for i := 0; i < 20; i++ {
		out := h.PickDistinct(3, false, "", "socks5", "", "")
		if len(out) < 2 {
			t.Fatalf("window shrank to %+v", out)
		}
		if last := out[len(out)-1]; last.Protocol != "socks5" {
			t.Fatalf("exit slot = %+v, want a socks5 proxy last in %+v", last, out)
		}
	}
}

// preferDistinctRegion keeps one proxy per region; with fewer regions than
// slots the window simply ends early.
func TestPickDistinctPrefersDistinctRegions(t *testing.T) {
	h := newHotCacheWith(t,
		hotProxy("10.1.1.1", 8080, "http", "US"),
		hotProxy("10.2.2.2", 8080, "http", "US"),
		hotProxy("10.3.3.3", 8080, "http", "JP"),
	)
	for i := 0; i < 20; i++ {
		out := h.PickDistinct(3, true, "", "", "", "")
		if len(out) != 2 {
			t.Fatalf("picked %+v, want one US and one JP", out)
		}
		regions := map[string]bool{}
		for _, p := range out {
			if regions[p.Region] {
				t.Fatalf("region %s picked twice in %+v", p.Region, out)
			}
			regions[p.Region] = true
		}
	}
}

// An exit region constraint admits only matching (or unknown-region) proxies to
// the last slot. Two JP proxies keep the outcome order-independent, the same
// way the exitProto test does.
func TestPickDistinctExitRegion(t *testing.T) {
	h := newHotCacheWith(t,
		hotProxy("10.1.1.1", 8080, "http", "US"),
		hotProxy("10.2.2.2", 8080, "http", "JP"),
		hotProxy("10.3.3.3", 8080, "http", "JP"),
	)
	for i := 0; i < 20; i++ {
		out := h.PickDistinct(3, false, "", "", "", "JP")
		if len(out) < 2 {
			t.Fatalf("window shrank to %+v", out)
		}
		if last := out[len(out)-1]; last.Region != "JP" {
			t.Fatalf("exit slot region = %q in %+v, want JP", last.Region, out)
		}
	}
}

// Nothing in the snapshot must not block a pick: an empty cache returns nil,
// which the caller treats as "descend to the store ladder".
func TestPickDistinctEmptyCache(t *testing.T) {
	h := newHotCacheWith(t)
	if out := h.PickDistinct(4, false, "", "", "", ""); len(out) != 0 {
		t.Fatalf("empty cache yielded %+v", out)
	}
}
