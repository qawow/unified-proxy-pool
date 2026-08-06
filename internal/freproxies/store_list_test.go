package freproxies

import (
	"context"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"
)

// seedScored inserts n proxies and puts them all in the scored (validated) set.
// Every 20th entry is IPv6 so a family filter matches a small minority, which is
// what real pools look like.
func seedScored(t *testing.T, s *redisStore, n int) {
	t.Helper()
	ctx := context.Background()
	batch := make([]Proxy, 0, n)
	for i := 0; i < n; i++ {
		// Validated must be set in the meta too: matchListFilter reads the
		// stored record, not zset membership, so OnlyOK would drop everything.
		p := Proxy{Port: 1080 + i, Protocol: "http", Source: "src", Region: "US",
			Validated: true, Score: float64(i % 101)}
		if i%20 == 0 {
			p.Host = fmt.Sprintf("2001:db8::%x", i+1)
		} else {
			p.Host = fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
		}
		batch = append(batch, p)
	}
	if _, err := s.AddRaw(ctx, batch); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	zs := make([]redis.Z, 0, n)
	for i, p := range batch {
		zs = append(zs, redis.Z{Score: float64(i % 101), Member: normalizeAddr(p.Host, p.Port)})
	}
	if err := s.rdb.ZAdd(ctx, keyScored, zs...).Err(); err != nil {
		t.Fatalf("ZAdd: %v", err)
	}
}

// A filtered query whose hydrate window saturates must not advertise the
// unfiltered set size as Total: the UI derives page count from Total, so an
// inflated value produces pages that render empty.
func TestRedisStoreListFilteredTotalHasNoPhantomPages(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	// 1000 > the 800 hydrate cap, so the window saturates.
	seedScored(t, s, 1000)

	const size = 20
	res, err := s.List(ctx, ListFilter{Page: 1, Size: size, OnlyOK: true, Family: FamilyIPv6})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Items) == 0 {
		t.Fatal("expected some IPv6 matches")
	}

	// Total must describe the filtered result, not the whole scored set.
	if res.Total >= 1000 {
		t.Errorf("Total = %d looks like the unfiltered count; filtered total should be far smaller", res.Total)
	}

	// Every page Total advertises must actually contain rows.
	pages := int((res.Total + size - 1) / size)
	for p := 1; p <= pages; p++ {
		got, err := s.List(ctx, ListFilter{Page: p, Size: size, OnlyOK: true, Family: FamilyIPv6})
		if err != nil {
			t.Fatalf("List(page=%d): %v", p, err)
		}
		if len(got.Items) == 0 {
			t.Errorf("page %d/%d advertised by Total=%d is empty", p, pages, res.Total)
		}
		for _, it := range got.Items {
			if it.Family() != FamilyIPv6 {
				t.Errorf("page %d contains non-ipv6 %s", p, it.Addr)
			}
		}
	}
}

// The unfiltered fast path still reports the true set size.
func TestRedisStoreListFastPathTotalIsExact(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	seedScored(t, s, 1000)

	res, err := s.List(ctx, ListFilter{Page: 1, Size: 20, OnlyOK: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Total != 1000 {
		t.Errorf("fast-path Total = %d, want 1000", res.Total)
	}
	if len(res.Items) != 20 {
		t.Errorf("page size = %d, want 20", len(res.Items))
	}
}

// Truncated must flag that matches may exist beyond the hydrate window, so the
// UI can distinguish "this is everything" from "this is the first N".
func TestRedisStoreListTruncatedFlag(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)

	// Small pool: window does not saturate, nothing is hidden.
	seedScored(t, s, 50)
	res, err := s.List(ctx, ListFilter{Page: 1, Size: 20, OnlyOK: true, Family: FamilyIPv6})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Truncated {
		t.Error("a non-saturated window must not be marked truncated")
	}

	// Large pool: window saturates, so results are a prefix of the matches.
	s2, _ := newFakeRedisStore(t)
	seedScored(t, s2, 1000)
	res2, err := s2.List(ctx, ListFilter{Page: 1, Size: 20, OnlyOK: true, Family: FamilyIPv6})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !res2.Truncated {
		t.Error("a saturated window must be marked truncated")
	}
}

// memoryStore and redisStore must agree on Total semantics for the same data,
// otherwise behaviour silently changes when Redis is unavailable.
func TestListTotalConsistentAcrossBackends(t *testing.T) {
	ctx := context.Background()
	rs, _ := newFakeRedisStore(t)
	ms := NewMemoryStore()

	batch := make([]Proxy, 0, 60)
	for i := 0; i < 60; i++ {
		p := Proxy{Port: 1080 + i, Protocol: "http", Source: "src", Region: "US"}
		if i%20 == 0 {
			p.Host = fmt.Sprintf("2001:db8::%x", i+1)
		} else {
			p.Host = fmt.Sprintf("10.0.%d.%d", i>>8&255, i&255)
		}
		batch = append(batch, p)
	}
	if _, err := rs.AddRaw(ctx, batch); err != nil {
		t.Fatalf("redis AddRaw: %v", err)
	}
	if _, err := ms.AddRaw(ctx, batch); err != nil {
		t.Fatalf("memory AddRaw: %v", err)
	}

	f := ListFilter{Page: 1, Size: 20, Family: FamilyIPv6}
	r1, err := rs.List(ctx, f)
	if err != nil {
		t.Fatalf("redis List: %v", err)
	}
	r2, err := ms.List(ctx, f)
	if err != nil {
		t.Fatalf("memory List: %v", err)
	}
	if r1.Total != r2.Total {
		t.Errorf("Total mismatch: redis=%d memory=%d", r1.Total, r2.Total)
	}
	if len(r1.Items) != len(r2.Items) {
		t.Errorf("item count mismatch: redis=%d memory=%d", len(r1.Items), len(r2.Items))
	}
}
