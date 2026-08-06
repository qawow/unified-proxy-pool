package freproxies

import (
	"context"
	"fmt"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

// randomCountingStore counts Random calls so tests can assert the service does
// not spin on a sampler that cannot satisfy the request.
type randomCountingStore struct {
	Store
	randomCalls int
	randomErr   error
}

func (r *randomCountingStore) Random(ctx context.Context, protocol string) (Proxy, error) {
	r.randomCalls++
	if r.randomErr != nil {
		return Proxy{}, r.randomErr
	}
	return r.Store.Random(ctx, protocol)
}

func newRandomService(t *testing.T) (*Service, *randomCountingStore) {
	t.Helper()
	rs := &randomCountingStore{Store: NewMemoryStore()}
	svc := NewService(rs, crawlers.NewRegistry(nil), nil, false)
	svc.hot = nil
	return svc, rs
}

// store.Random knows nothing about region or family, so its error only means
// "this sampler found nothing" — not "the pool has nothing". The service must
// still descend to the relaxation ladder, which can drop the protocol
// constraint and succeed.
func TestRandomFilterFallsBackWhenSamplerErrors(t *testing.T) {
	ctx := context.Background()
	svc, rs := newRandomService(t)

	if _, err := svc.store.AddRaw(ctx, []Proxy{
		{Host: "10.0.0.1", Port: 8080, Protocol: "http"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	// Simulate a sampler that cannot serve this request at all.
	rs.randomErr = fmt.Errorf("no proxy available")

	got, err := svc.RandomFamilyFilter(ctx, "socks5", "", "")
	if err != nil {
		t.Fatalf("expected the ladder to relax protocol and serve a proxy, got %v", err)
	}
	if got.Addr == "" {
		t.Fatal("returned an empty proxy with a nil error")
	}
}

// A genuinely empty pool must still fail rather than return a zero Proxy.
func TestRandomFilterFailsOnEmptyPool(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRandomService(t)

	got, err := svc.RandomFamilyFilter(ctx, "", "", "")
	if err == nil {
		t.Fatalf("expected an error for an empty pool, got %+v", got)
	}
}

// store.Random cannot filter by family, so retrying it for a rare family is
// near-guaranteed to miss: it samples the same few lowest-latency proxies each
// time. The service must not burn a full round of Redis calls before descending
// to the ladder, which pushes family down into the store.
func TestRandomFilterDoesNotSpinSamplerForFamily(t *testing.T) {
	ctx := context.Background()
	svc, rs := newRandomService(t)

	// One IPv6 proxy that only a family-aware query can find, plus IPv4 noise
	// that the sampler will keep returning.
	seed := []Proxy{{Host: "2001:db8::1", Port: 1080, Protocol: "http"}}
	for i := 0; i < 5; i++ {
		seed = append(seed, Proxy{Host: fmt.Sprintf("10.0.0.%d", i+1), Port: 8080 + i, Protocol: "http"})
	}
	if _, err := svc.store.AddRaw(ctx, seed); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}

	got, err := svc.RandomFamilyFilter(ctx, "", "", FamilyIPv6)
	if err != nil {
		t.Fatalf("RandomFamilyFilter(ipv6): %v", err)
	}
	if got.Family() != FamilyIPv6 {
		t.Fatalf("asked for ipv6, got %s (%s)", got.Family(), got.Addr)
	}
	if rs.randomCalls > 1 {
		t.Errorf("sampled the family-blind store %d times; family requests should go straight to the ladder", rs.randomCalls)
	}
}

// The blacklist retry loop still has to work for the unconstrained case.
func TestRandomFilterSkipsBlacklisted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRandomService(t)

	if _, err := svc.store.AddRaw(ctx, []Proxy{
		{Host: "10.0.0.1", Port: 8080, Protocol: "http"},
		{Host: "10.0.0.2", Port: 8081, Protocol: "http"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	blockedAddr := normalizeAddr("10.0.0.1", 8080)
	svc.SetBlockedFn(func(addr string) bool { return addr == blockedAddr })

	for i := 0; i < 10; i++ {
		got, err := svc.RandomFamilyFilter(ctx, "", "", "")
		if err != nil {
			t.Fatalf("RandomFamilyFilter: %v", err)
		}
		if got.Addr == blockedAddr {
			t.Fatalf("served a blacklisted proxy: %s", got.Addr)
		}
	}
}
