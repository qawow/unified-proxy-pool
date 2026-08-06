package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// statsFullScan mirrors the pre-optimization stats acquisition: two ZCARDs plus
// a full ZREVRANGE WITHSCORES, bucketed client-side.
func (s *redisStore) statsFullScan(ctx context.Context) (int64, int64, map[string]int64, []string, error) {
	raw, err := s.rdb.ZCard(ctx, keyRaw).Result()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	validated, err := s.rdb.ZCard(ctx, keyScored).Result()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	members, err := s.rdb.ZRevRangeWithScores(ctx, keyScored, 0, -1).Result()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	buckets := map[string]int64{"0-20": 0, "21-50": 0, "51-80": 0, "81-100": 0}
	sample := make([]string, 0, queueSampleSize)
	for i, z := range members {
		switch {
		case z.Score <= 20:
			buckets["0-20"]++
		case z.Score <= 50:
			buckets["21-50"]++
		case z.Score <= 80:
			buckets["51-80"]++
		default:
			buckets["81-100"]++
		}
		if i < queueSampleSize {
			if addr, ok := z.Member.(string); ok {
				sample = append(sample, addr)
			}
		}
	}
	return raw, validated, buckets, sample, nil
}

// Benchmarks run against an in-process miniredis, so they never touch a real
// Redis server and no database is ever flushed.
//
// Note on interpretation: miniredis speaks over a local socket, so absolute
// ns/op understates the gain against a networked Redis (where per-command
// round-trips dominate). The B/op and allocs/op figures are the meaningful
// signal here — they show how much data the client still materializes.

func newBenchRedis(b *testing.B, validated int) *redisStore {
	b.Helper()
	mr := miniredis.RunT(b)
	s, err := OpenRedis(mr.Addr(), "", 0)
	if err != nil {
		b.Fatalf("OpenRedis: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	rs := s.(*redisStore)

	ctx := context.Background()
	batch := make([]Proxy, 0, validated)
	for i := 0; i < validated; i++ {
		p := Proxy{
			Port:     1080 + i,
			Protocol: []string{"http", "https", "socks5"}[i%3],
			Source:   fmt.Sprintf("src%02d", i%20),
			Region:   []string{"US", "JP", "DE", "CN", "SG"}[i%5],
		}
		if i%4 == 0 {
			p.Host = fmt.Sprintf("2001:db8::%x", i+1)
		} else {
			p.Host = fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
		}
		batch = append(batch, p)
	}
	if _, err := rs.AddRaw(ctx, batch); err != nil {
		b.Fatalf("AddRaw: %v", err)
	}
	// Promote all into the scored set with spread-out scores so every bucket
	// is populated.
	zs := make([]redis.Z, 0, validated)
	for i, p := range batch {
		zs = append(zs, redis.Z{Score: float64(i % 101), Member: normalizeAddr(p.Host, p.Port)})
	}
	if err := rs.rdb.ZAdd(ctx, keyScored, zs...).Err(); err != nil {
		b.Fatalf("ZAdd: %v", err)
	}
	return rs
}

func BenchmarkQueues(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rs.Queues(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkQueuesFullScan measures the stats-acquisition half of the old Queues
// (ZCARD x2 + full ZREVRANGE WITHSCORES) against the new pipelined ZCOUNT form,
// isolating it from the shared mgetProxies sample cost.
func BenchmarkQueuesFullScan(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, _, err := rs.statsFullScan(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkQueuesStatsOnly is the new server-side path, same scope as above.
func BenchmarkQueuesStatsOnly(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pipe := rs.rdb.Pipeline()
		pipe.ZCard(ctx, keyRaw)
		pipe.ZCard(ctx, keyScored)
		for _, r := range [][2]string{{"-inf", "20"}, {"(20", "50"}, {"(50", "80"}, {"(80", "+inf"}} {
			pipe.ZCount(ctx, keyScored, r[0], r[1])
		}
		pipe.ZRevRange(ctx, keyScored, 0, queueSampleSize-1)
		if _, err := pipe.Exec(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAvgScore(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rs.AvgScore(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMgetProxies300(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	addrs, err := rs.rdb.ZRevRange(ctx, keyScored, 0, 299).Result()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := rs.mgetProxies(ctx, addrs); len(out) == 0 {
			b.Fatal("expected proxies")
		}
	}
}

// mgetProxiesPipelined is the pre-optimization implementation (N individual
// GETs in a pipeline), kept for comparison against the single MGET.
func (s *redisStore) mgetProxiesPipelined(ctx context.Context, addrs []string) []Proxy {
	if len(addrs) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(addrs))
	for i, addr := range addrs {
		cmds[i] = pipe.Get(ctx, s.metaKey(addr))
	}
	_, _ = pipe.Exec(ctx)
	out := make([]Proxy, 0, len(addrs))
	for i, cmd := range cmds {
		raw, err := cmd.Bytes()
		if err != nil {
			continue
		}
		var p Proxy
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if p.Addr == "" {
			p.Addr = addrs[i]
		}
		out = append(out, p)
	}
	return out
}

func BenchmarkMgetProxies300Pipelined(b *testing.B) {
	rs := newBenchRedis(b, MaxValidatedProxies)
	ctx := context.Background()
	addrs, err := rs.rdb.ZRevRange(ctx, keyScored, 0, 299).Result()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := rs.mgetProxiesPipelined(ctx, addrs); len(out) == 0 {
			b.Fatal("expected proxies")
		}
	}
}
