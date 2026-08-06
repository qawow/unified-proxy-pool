package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// keyYieldHist is the per-source history ZSET prefix: score is the
	// measurement's unix time, member is the record JSON. A ZSET per source
	// replaces a KEYS scan — KEYS walks the entire keyspace and blocks Redis's
	// single-threaded event loop for every other client, the same reason the
	// Lua AvgScore aggregation was reverted.
	keyYieldHist = "upp:sourceyield:h:"
	// keyYieldSources indexes which sources have history, so building the
	// summary is one SMEMBERS instead of a keyspace scan.
	keyYieldSources = "upp:sourceyield:sources"

	// yieldRetention drops individual measurements older than this.
	yieldRetention = 90 * 24 * time.Hour
	// yieldMaxPerSource caps history per source regardless of age.
	yieldMaxPerSource = 500
	// yieldTrendWindow is how many measurements a trend considers by default.
	yieldTrendWindow = 5
	// yieldTrendDelta is how far the mean rate must move to call a direction.
	yieldTrendDelta = 0.05
)

// Trend verdicts. INSUFFICIENT is distinct from STABLE on purpose: one
// measurement is not evidence of steadiness.
const (
	TrendInsufficient = "INSUFFICIENT"
	TrendImproving    = "IMPROVING"
	TrendDegrading    = "DEGRADING"
	TrendStable       = "STABLE"
)

// SourceYieldRecord is one measurement of how many *working* proxies a source
// contributes. Fetched is what it published; Sampled/Alive is what was dialed.
type SourceYieldRecord struct {
	Source    string    `json:"source"`
	MeasureAt time.Time `json:"measure_at"`
	Fetched   int       `json:"fetched"`
	Sampled   int       `json:"sampled"`
	Alive     int       `json:"alive"`
	Estimate  int       `json:"estimate"` // Rate() projected onto Fetched
	Action    string    `json:"action"`   // KEEP / DISABLE / OVERSIZED / UNKNOWN
	Why       string    `json:"why"`
}

// Rate is the measured survival rate over the sample.
func (r SourceYieldRecord) Rate() float64 {
	if r.Sampled == 0 {
		return 0
	}
	return float64(r.Alive) / float64(r.Sampled)
}

// ClassifyTrend compares the mean rate of the newer half against the older
// half. Records must arrive newest-first.
//
// Fewer than two measurements is INSUFFICIENT rather than STABLE: a single
// sample says nothing about direction, and reporting "stable" from one point
// would invite disabling a source on one bad network night.
//
// Exported so the report and tune commands classify identically — two
// implementations would eventually disagree about whether a source is degrading.
func ClassifyTrend(records []SourceYieldRecord) string {
	if len(records) < 2 {
		return TrendInsufficient
	}
	mid := len(records) / 2
	mean := func(rs []SourceYieldRecord) float64 {
		if len(rs) == 0 {
			return 0
		}
		var sum float64
		for _, r := range rs {
			sum += r.Rate()
		}
		return sum / float64(len(rs))
	}
	delta := mean(records[:mid]) - mean(records[mid:])
	switch {
	case delta > yieldTrendDelta:
		return TrendImproving
	case delta < -yieldTrendDelta:
		return TrendDegrading
	default:
		return TrendStable
	}
}

// sortRecordsNewestFirst orders history for trend analysis and display.
func sortRecordsNewestFirst(recs []SourceYieldRecord) {
	sort.Slice(recs, func(i, j int) bool { return recs[i].MeasureAt.After(recs[j].MeasureAt) })
}

func (s *redisStore) yieldKey(source string) string { return keyYieldHist + source }

// SaveSourceYield appends one measurement to the source's history and prunes
// what has aged out.
func (s *redisStore) SaveSourceYield(ctx context.Context, rec SourceYieldRecord) error {
	if rec.Source == "" {
		return fmt.Errorf("source yield record needs a source name")
	}
	if rec.MeasureAt.IsZero() {
		rec.MeasureAt = time.Now().UTC()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := s.yieldKey(rec.Source)
	cutoff := rec.MeasureAt.Add(-yieldRetention).Unix()

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(rec.MeasureAt.Unix()), Member: string(raw)})
	pipe.SAdd(ctx, keyYieldSources, rec.Source)
	// Age-based prune, then count-based: either alone lets the other grow.
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("(%d", cutoff))
	pipe.ZRemRangeByRank(ctx, key, 0, -(yieldMaxPerSource + 1))
	// Refresh the TTL so a source that stops being measured eventually expires
	// instead of pinning history forever.
	pipe.Expire(ctx, key, yieldRetention)
	_, err = pipe.Exec(ctx)
	return err
}

// ListSourceYield returns a source's measurements, newest first.
func (s *redisStore) ListSourceYield(ctx context.Context, source string, limit int) ([]SourceYieldRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	// ZREVRANGE reads the newest `limit` directly; no keyspace scan, and the
	// server does the ordering.
	members, err := s.rdb.ZRevRange(ctx, s.yieldKey(source), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]SourceYieldRecord, 0, len(members))
	for _, m := range members {
		var rec SourceYieldRecord
		if err := json.Unmarshal([]byte(m), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sortRecordsNewestFirst(out)
	return out, nil
}

// AllSourceYieldSummary returns the newest measurement per source.
func (s *redisStore) AllSourceYieldSummary(ctx context.Context) (map[string]SourceYieldRecord, error) {
	sources, err := s.rdb.SMembers(ctx, keyYieldSources).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]SourceYieldRecord, len(sources))
	if len(sources) == 0 {
		return out, nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make(map[string]*redis.StringSliceCmd, len(sources))
	for _, src := range sources {
		cmds[src] = pipe.ZRevRange(ctx, s.yieldKey(src), 0, 0)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	var stale []string
	for src, cmd := range cmds {
		members, err := cmd.Result()
		if err != nil || len(members) == 0 {
			// History expired but the index still names it; clean that up so the
			// index does not grow forever with sources that no longer report.
			stale = append(stale, src)
			continue
		}
		var rec SourceYieldRecord
		if err := json.Unmarshal([]byte(members[0]), &rec); err != nil {
			continue
		}
		out[src] = rec
	}
	if len(stale) > 0 {
		_ = s.rdb.SRem(ctx, keyYieldSources, stale).Err()
	}
	return out, nil
}

// SourceYieldTrend reports whether a source is improving, degrading, or steady.
func (s *redisStore) SourceYieldTrend(ctx context.Context, source string, window int) (string, []SourceYieldRecord, error) {
	if window <= 0 {
		window = yieldTrendWindow
	}
	records, err := s.ListSourceYield(ctx, source, window)
	if err != nil {
		return "", nil, err
	}
	return ClassifyTrend(records), records, nil
}

// --- memoryStore ---
//
// History lives in its own map, not in the event ring. Sharing the ring leaked
// record JSON into the dashboard feed (Service.Overview renders RecentEvents
// verbatim) and let 50 ordinary events erase the history a trend reads.

func (s *memoryStore) SaveSourceYield(ctx context.Context, rec SourceYieldRecord) error {
	if rec.Source == "" {
		return fmt.Errorf("source yield record needs a source name")
	}
	if rec.MeasureAt.IsZero() {
		rec.MeasureAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.yields == nil {
		s.yields = map[string][]SourceYieldRecord{}
	}
	recs := append(s.yields[rec.Source], rec)
	sortRecordsNewestFirst(recs)
	cutoff := rec.MeasureAt.Add(-yieldRetention)
	kept := recs[:0]
	for _, r := range recs {
		if r.MeasureAt.Before(cutoff) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) > yieldMaxPerSource {
		kept = kept[:yieldMaxPerSource]
	}
	s.yields[rec.Source] = kept
	return nil
}

func (s *memoryStore) ListSourceYield(ctx context.Context, source string, limit int) ([]SourceYieldRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	recs := s.yields[source]
	out := make([]SourceYieldRecord, 0, len(recs))
	out = append(out, recs...)
	sortRecordsNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) AllSourceYieldSummary(ctx context.Context) (map[string]SourceYieldRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]SourceYieldRecord, len(s.yields))
	for src, recs := range s.yields {
		if len(recs) == 0 {
			continue
		}
		newest := recs[0]
		for _, r := range recs[1:] {
			if r.MeasureAt.After(newest.MeasureAt) {
				newest = r
			}
		}
		out[src] = newest
	}
	return out, nil
}

func (s *memoryStore) SourceYieldTrend(ctx context.Context, source string, window int) (string, []SourceYieldRecord, error) {
	if window <= 0 {
		window = yieldTrendWindow
	}
	records, err := s.ListSourceYield(ctx, source, window)
	if err != nil {
		return "", nil, err
	}
	return ClassifyTrend(records), records, nil
}
