package freproxies

import (
	"context"
	"fmt"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

// countingStore records every List filter it is asked for, so tests can assert
// the fallback ladder does not repeat an identical (expensive) query.
type countingStore struct {
	Store
	listFilters []ListFilter
}

func (c *countingStore) List(ctx context.Context, f ListFilter) (ListResult, error) {
	c.listFilters = append(c.listFilters, f)
	return c.Store.List(ctx, f)
}

// RandomN reports an empty pool so every test reaches the List ladder. This
// matches the production store: redisStore.RandomN reads only the scored set and
// requires Validated || Score >= ScoreInit, so an unvalidated pool sends the
// request down the ladder. memoryStore is deliberately more lenient (it samples
// the raw pool as a fallback), which would otherwise short-circuit these tests.
func (c *countingStore) RandomN(ctx context.Context, protocol string, n int) ([]Proxy, error) {
	return nil, fmt.Errorf("no proxy available")
}

// key identifies a query by the dimensions that reach Redis.
func listKey(f ListFilter) string {
	return fmt.Sprintf("proto=%s|region=%s|family=%s|onlyok=%v|size=%d",
		f.Protocol, f.Region, f.Family, f.OnlyOK, f.Size)
}

func newCountingService(t *testing.T) (*Service, *countingStore) {
	t.Helper()
	cs := &countingStore{Store: NewMemoryStore()}
	svc := NewService(cs, crawlers.NewRegistry(nil), nil, false)
	// Disable the hot cache so the fallback ladder is exercised deterministically.
	svc.hot = nil
	return svc, cs
}

// When no proxy can satisfy the request, the ladder must not issue the same
// query twice — each retry should relax at least one dimension.
func TestPickFallbackLadderHasNoDuplicateQueries(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name                     string
		protocol, region, family string
	}{
		// The API added for IPv6 selection: protocol and region are empty, so
		// levels 1 and 2 collapse onto the same filter unless deduped.
		{"family only", "", "", FamilyIPv6},
		{"protocol only", "socks5", "", ""},
		{"family+protocol", "socks5", "", FamilyIPv6},
		{"all empty", "", "", ""},
		{"region only", "", "US", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, cs := newCountingService(t)
			// Seed one proxy that cannot match any case above, forcing the full
			// ladder to run: IPv4 http, region DE, and never validated.
			if _, err := svc.store.AddRaw(ctx, []Proxy{
				{Host: "10.9.9.9", Port: 1, Protocol: "http", Region: "DE"},
			}); err != nil {
				t.Fatalf("AddRaw: %v", err)
			}

			// Expected to fail; we only care about the queries it made.
			_, _ = svc.PickValidatedNFilter(ctx, tc.protocol, tc.region, tc.family, 1)

			seen := map[string]int{}
			for _, f := range cs.listFilters {
				seen[listKey(f)]++
			}
			for k, n := range seen {
				if n > 1 {
					t.Errorf("filter %s issued %d times; each fallback step must relax a dimension", k, n)
				}
			}
		})
	}
}

// Family must never be relaxed by the fallback ladder: returning an IPv4 proxy
// to a caller that explicitly asked for IPv6 would silently break them.
func TestPickNeverRelaxesFamily(t *testing.T) {
	ctx := context.Background()
	svc, cs := newCountingService(t)

	// Pool has only IPv4, so an IPv6 request must fail rather than fall back.
	if _, err := svc.store.AddRaw(ctx, []Proxy{
		{Host: "10.0.0.1", Port: 1, Protocol: "http"},
		{Host: "10.0.0.2", Port: 2, Protocol: "socks5"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}

	got, err := svc.PickValidatedNFilter(ctx, "", "", FamilyIPv6, 1)
	if err == nil {
		t.Fatalf("expected failure for ipv6 request against ipv4-only pool, got %+v", got)
	}
	// Every store query must have carried the family constraint.
	for _, f := range cs.listFilters {
		if f.Family != FamilyIPv6 {
			t.Errorf("fallback dropped the family constraint: %+v", f)
		}
	}
}

// PickValidatedFilter indexes items[0] without a length check, and the
// directproxy callers range over the slice assuming a nil error means at least
// one entry. Pin that contract down: never (empty, nil).
func TestPickNeverReturnsEmptySliceWithNilError(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name             string
		seed             []Proxy
		protocol, family string
	}{
		{"empty pool", nil, "", ""},
		{"empty pool, family asked", nil, "", FamilyIPv6},
		{"no family match", []Proxy{{Host: "10.0.0.1", Port: 1, Protocol: "http"}}, "", FamilyIPv6},
		{"no protocol match", []Proxy{{Host: "10.0.0.1", Port: 1, Protocol: "http"}}, "socks5", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newCountingService(t)
			if len(tc.seed) > 0 {
				if _, err := svc.store.AddRaw(ctx, tc.seed); err != nil {
					t.Fatalf("AddRaw: %v", err)
				}
			}
			out, err := svc.PickValidatedNFilter(ctx, tc.protocol, "", tc.family, 1)
			if err == nil && len(out) == 0 {
				t.Fatal("returned an empty slice with a nil error; callers index out[0]")
			}
		})
	}
}

// A request the most specific query can satisfy must short-circuit there.
func TestPickStopsAtFirstSuccess(t *testing.T) {
	ctx := context.Background()
	svc, cs := newCountingService(t)

	if _, err := svc.store.AddRaw(ctx, []Proxy{
		{Host: "2001:db8::1", Port: 1, Protocol: "socks5", Region: "US"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	// Level 1 carries OnlyOK, so the proxy has to be validated to match it.
	if err := svc.store.MarkValidated(ctx, normalizeAddr("2001:db8::1", 1), 20, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}

	out, err := svc.PickValidatedNFilter(ctx, "socks5", "US", FamilyIPv6, 1)
	if err != nil {
		t.Fatalf("PickValidatedNFilter: %v", err)
	}
	if len(out) != 1 || out[0].Family() != FamilyIPv6 {
		t.Fatalf("unexpected pick: %+v", out)
	}
	if len(cs.listFilters) != 1 {
		t.Errorf("expected to stop after 1 List, made %d: %v", len(cs.listFilters), cs.listFilters)
	}
}

// A page full of proxies the service-side filters reject must not end the
// search: blacklist and disabled-source state live in the service, so the store
// happily returns rows that are unusable. The ladder has to keep descending.
func TestPickDescendsWhenPageIsFullyFilteredOut(t *testing.T) {
	ctx := context.Background()
	svc, cs := newCountingService(t)

	const bad = "2001:db8::bad"
	if _, err := svc.store.AddRaw(ctx, []Proxy{
		// Matches the most specific query, but is blacklisted.
		{Host: bad, Port: 1, Protocol: "socks5", Region: "US"},
		// Only the last, fully relaxed query can reach this one: different
		// protocol, no region, and left unvalidated.
		{Host: "2001:db8::2", Port: 2, Protocol: "http"},
	}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	blockedAddr := normalizeAddr(bad, 1)
	if err := svc.store.MarkValidated(ctx, blockedAddr, 10, true); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}
	if err := svc.store.MarkValidated(ctx, normalizeAddr("2001:db8::2", 2), 10, true); err != nil {
		t.Fatalf("MarkValidated fallback: %v", err)
	}
	svc.SetBlockedFn(func(addr string) bool { return addr == blockedAddr })

	out, err := svc.PickValidatedNFilter(ctx, "socks5", "US", FamilyIPv6, 1)
	if err != nil {
		t.Fatalf("PickValidatedNFilter: %v", err)
	}
	if len(out) != 1 || out[0].Addr == blockedAddr {
		t.Fatalf("expected the non-blacklisted proxy, got %+v", out)
	}
	if len(cs.listFilters) < 2 {
		t.Errorf("expected the ladder to descend past the filtered-out page, made %d queries", len(cs.listFilters))
	}
}
