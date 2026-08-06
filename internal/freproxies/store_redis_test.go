package freproxies

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// redisZ is a small constructor to keep ZAdd calls readable in tests.
func redisZ(member string, score float64) redis.Z {
	return redis.Z{Score: score, Member: member}
}

// newFakeRedisStore returns a redisStore backed by an in-process miniredis.
// Nothing touches a real Redis server, so tests are safe to run anywhere.
func newFakeRedisStore(t *testing.T) (*redisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := OpenRedis(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("OpenRedis(miniredis): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*redisStore), mr
}

// seedRedisValidated adds n proxies and marks them validated with known scores.
func seedRedisValidated(t *testing.T, s *redisStore, n int) []Proxy {
	t.Helper()
	ctx := context.Background()
	batch := make([]Proxy, 0, n)
	for i := 0; i < n; i++ {
		p := Proxy{
			Port:     1080 + i,
			Protocol: []string{"http", "https", "socks5"}[i%3],
			Source:   fmt.Sprintf("src%d", i%3),
			Region:   []string{"US", "JP", "DE"}[i%3],
		}
		// Every 4th entry is IPv6 so family accounting is exercised.
		if i%4 == 0 {
			p.Host = fmt.Sprintf("2001:db8::%x", i+1)
		} else {
			p.Host = fmt.Sprintf("10.0.0.%d", i+1)
		}
		batch = append(batch, p)
	}
	if _, err := s.AddRaw(ctx, batch); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	return batch
}

func TestRedisStoreMgetProxies(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	seedRedisValidated(t, s, 5)

	addrs := []string{
		"[2001:db8::1]:1080", // IPv6 stays bracketed
		"10.0.0.2:1081",
		"10.0.0.99:9999", // missing key must be skipped, not zero-filled
	}
	got := s.mgetProxies(ctx, addrs)
	if len(got) != 2 {
		t.Fatalf("mgetProxies returned %d proxies, want 2 (missing key skipped)", len(got))
	}
	for _, p := range got {
		if p.Addr == "" {
			t.Error("Addr should be populated")
		}
		if p.IPFamily == "" {
			t.Errorf("IPFamily should be set on ingest for %s", p.Addr)
		}
	}
	// Empty input must not issue a command.
	if out := s.mgetProxies(ctx, nil); out != nil {
		t.Errorf("mgetProxies(nil) = %v, want nil", out)
	}
}

func TestRedisStoreAvgScore(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)

	// Empty set averages to 0 rather than dividing by zero.
	if avg, err := s.AvgScore(ctx); err != nil || avg != 0 {
		t.Fatalf("AvgScore(empty) = %v, %v; want 0, nil", avg, err)
	}

	seedRedisValidated(t, s, 4)
	scores := []float64{10, 20, 30, 40}
	addrs := []string{"[2001:db8::1]:1080", "10.0.0.2:1081", "10.0.0.3:1082", "10.0.0.4:1083"}
	for i, addr := range addrs {
		if err := s.rdb.ZAdd(ctx, keyScored, redisZ(addr, scores[i])).Err(); err != nil {
			t.Fatalf("ZAdd: %v", err)
		}
	}

	avg, err := s.AvgScore(ctx)
	if err != nil {
		t.Fatalf("AvgScore: %v", err)
	}
	if math.Abs(avg-25) > 0.001 {
		t.Errorf("AvgScore = %v, want 25", avg)
	}
}

func TestRedisStoreQueuesBucketBoundaries(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)

	// Scores sit exactly on the bucket edges to pin the inclusive/exclusive
	// semantics: (-inf,20] (20,50] (50,80] (80,+inf).
	edge := map[string]float64{
		"10.0.0.1:1": 20,  // 0-20
		"10.0.0.2:2": 21,  // 21-50
		"10.0.0.3:3": 50,  // 21-50
		"10.0.0.4:4": 51,  // 51-80
		"10.0.0.5:5": 80,  // 51-80
		"10.0.0.6:6": 81,  // 81-100
		"10.0.0.7:7": 100, // 81-100
	}
	for addr, sc := range edge {
		if err := s.rdb.ZAdd(ctx, keyScored, redisZ(addr, sc)).Err(); err != nil {
			t.Fatalf("ZAdd: %v", err)
		}
	}

	q, err := s.Queues(ctx)
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	want := map[string]int64{"0-20": 1, "21-50": 2, "51-80": 2, "81-100": 2}
	for label, n := range want {
		if q.ScoreBuckets[label] != n {
			t.Errorf("bucket %s = %d, want %d", label, q.ScoreBuckets[label], n)
		}
	}
	if q.ValidatedCount != 7 {
		t.Errorf("ValidatedCount = %d, want 7", q.ValidatedCount)
	}
}

func TestRedisStoreQueuesFamilyAndProtocolCounts(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	seedRedisValidated(t, s, 8)

	// Promote everything into the scored set so the sample covers all entries.
	for i := 0; i < 8; i++ {
		var addr string
		if i%4 == 0 {
			addr = fmt.Sprintf("[2001:db8::%x]:%d", i+1, 1080+i)
		} else {
			addr = fmt.Sprintf("10.0.0.%d:%d", i+1, 1080+i)
		}
		if err := s.MarkValidated(ctx, addr, 100, true); err != nil {
			t.Fatalf("MarkValidated(%s): %v", addr, err)
		}
	}

	q, err := s.Queues(ctx)
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	// i%4==0 → indices 0 and 4 are IPv6.
	if q.FamilyCounts[FamilyIPv6] != 2 {
		t.Errorf("ipv6 count = %d, want 2", q.FamilyCounts[FamilyIPv6])
	}
	if q.FamilyCounts[FamilyIPv4] != 6 {
		t.Errorf("ipv4 count = %d, want 6", q.FamilyCounts[FamilyIPv4])
	}
	total := int64(0)
	for _, v := range q.ProtocolCounts {
		total += v
	}
	if total != 8 {
		t.Errorf("protocol counts sum = %d, want 8", total)
	}
}

func TestRedisStoreQueuesEmpty(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	q, err := s.Queues(ctx)
	if err != nil {
		t.Fatalf("Queues(empty): %v", err)
	}
	if q.RawCount != 0 || q.ValidatedCount != 0 {
		t.Errorf("empty store should report zero counts, got %+v", q)
	}
	// All four buckets must still be present so the UI can render them.
	for _, label := range []string{"0-20", "21-50", "51-80", "81-100"} {
		if _, ok := q.ScoreBuckets[label]; !ok {
			t.Errorf("bucket %q missing from empty result", label)
		}
	}
}

func TestRedisStoreListFamilyFilter(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)
	seedRedisValidated(t, s, 8)

	v6, err := s.List(ctx, ListFilter{Page: 1, Size: 20, Family: FamilyIPv6})
	if err != nil {
		t.Fatalf("List(ipv6): %v", err)
	}
	if len(v6.Items) != 2 {
		t.Errorf("ipv6 items = %d, want 2", len(v6.Items))
	}
	for _, p := range v6.Items {
		if p.Family() != FamilyIPv6 {
			t.Errorf("got %s with family %s in ipv6 filter", p.Addr, p.Family())
		}
	}

	v4, err := s.List(ctx, ListFilter{Page: 1, Size: 20, Family: FamilyIPv4})
	if err != nil {
		t.Fatalf("List(ipv4): %v", err)
	}
	if len(v4.Items) != 6 {
		t.Errorf("ipv4 items = %d, want 6", len(v4.Items))
	}
}

func TestRedisStoreGroupCRUD(t *testing.T) {
	ctx := context.Background()
	s, _ := newFakeRedisStore(t)

	// Builtins resolve with no stored records.
	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != len(BuiltinGroups()) {
		t.Fatalf("got %d groups, want %d builtins", len(groups), len(BuiltinGroups()))
	}

	custom := ProxyGroup{Name: "v6fast", Label: "v6", Rule: GroupRule{Families: []string{FamilyIPv6}, MinScore: 30}}
	if err := s.SaveGroup(ctx, custom); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	got, err := s.GetGroup(ctx, "v6fast")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if got.Builtin {
		t.Error("custom group must not be flagged builtin")
	}
	if got.Rule.MinScore != 30 {
		t.Errorf("rule not round-tripped: %+v", got.Rule)
	}
	if err := s.DeleteGroup(ctx, "v6fast"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := s.GetGroup(ctx, "v6fast"); err == nil {
		t.Error("deleted group should not resolve")
	}
}
