package freproxies

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Yield records must not surface in the dashboard's event feed.
//
// memoryStore keeps events in a slice that Service.Overview reads via
// RecentEvents(15) and renders verbatim. Storing yield measurements in that same
// slice would push operator-facing lines ("scrape round finished", "validator
// promoted N") out of a 50-entry ring and replace them with raw JSON.
func TestSaveSourceYieldDoesNotPolluteEventFeed(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.PushEvent(ctx, "scrape round finished: 128 new"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rec := SourceYieldRecord{
			Source:    "alpha",
			MeasureAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
			Fetched:   100,
			Sampled:   60,
			Alive:     12,
			Action:    "KEEP",
		}
		if err := store.SaveSourceYield(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	events, err := store.RecentEvents(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev, "sourceyield") || strings.Contains(ev, `"fetched"`) {
			t.Errorf("a yield record leaked into the dashboard event feed: %q", ev)
		}
	}
	if len(events) != 1 {
		t.Errorf("expected only the 1 real event, got %d: %v", len(events), events)
	}
}

// The 50-entry event ring must not be consumable by yield writes: enough
// measurements would evict every real event before an operator reads them.
func TestSourceYieldWritesDoNotEvictRealEvents(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.PushEvent(ctx, "validator promoted 42"); err != nil {
		t.Fatal(err)
	}
	// More measurements than the event ring holds.
	for i := 0; i < 60; i++ {
		rec := SourceYieldRecord{
			Source:    "beta",
			MeasureAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
			Sampled:   10,
			Alive:     1,
		}
		if err := store.SaveSourceYield(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	events, err := store.RecentEvents(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range events {
		if strings.Contains(ev, "validator promoted 42") {
			found = true
		}
	}
	if !found {
		t.Error("60 yield writes evicted the real event out of the ring")
	}
}

// The Redis path is what production uses, and it is a different implementation
// from memoryStore — ZSETs per source plus a source index, replacing the KEYS
// scan that would have blocked Redis's event loop. Passing memory tests say
// nothing about it, so both backends run the same checks.
func TestSourceYieldParityAcrossBackends(t *testing.T) {
	backends := map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemoryStore() },
		"redis": func(t *testing.T) Store {
			s, _ := newFakeRedisStore(t)
			return s
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			store := open(t)
			ctx := context.Background()
			base := time.Now().UTC()

			// alpha improves, beta stays flat.
			for i, alive := range []int{10, 15, 20, 25, 30, 35} {
				if err := store.SaveSourceYield(ctx, SourceYieldRecord{
					Source:    "alpha",
					MeasureAt: base.Add(-time.Duration(6-i) * time.Hour),
					Fetched:   100, Sampled: 100, Alive: alive, Estimate: alive,
					Action: "KEEP",
				}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				if err := store.SaveSourceYield(ctx, SourceYieldRecord{
					Source:    "beta",
					MeasureAt: base.Add(-time.Duration(3-i) * time.Hour),
					Fetched:   50, Sampled: 50, Alive: 10, Estimate: 10,
					Action: "KEEP",
				}); err != nil {
					t.Fatal(err)
				}
			}

			// Newest-first ordering is what trend classification depends on.
			recs, err := store.ListSourceYield(ctx, "alpha", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 6 {
				t.Fatalf("expected 6 records, got %d", len(recs))
			}
			if recs[0].Alive != 35 {
				t.Errorf("records must come back newest-first; newest Alive = %d, want 35", recs[0].Alive)
			}
			for i := 1; i < len(recs); i++ {
				if recs[i].MeasureAt.After(recs[i-1].MeasureAt) {
					t.Fatalf("records out of order at %d", i)
				}
			}

			// limit must cap from the newest end, not the oldest.
			recs, err = store.ListSourceYield(ctx, "alpha", 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 2 || recs[0].Alive != 35 || recs[1].Alive != 30 {
				t.Errorf("limit should keep the 2 newest (35, 30), got %+v", aliveOf(recs))
			}

			summary, err := store.AllSourceYieldSummary(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(summary) != 2 {
				t.Fatalf("expected 2 sources in the summary, got %d", len(summary))
			}
			if summary["alpha"].Alive != 35 {
				t.Errorf("summary must hold the newest measurement, got Alive=%d", summary["alpha"].Alive)
			}

			trend, _, err := store.SourceYieldTrend(ctx, "alpha", 6)
			if err != nil {
				t.Fatal(err)
			}
			if trend != TrendImproving {
				t.Errorf("alpha trend = %s, want %s", trend, TrendImproving)
			}
			if trend, _, _ := store.SourceYieldTrend(ctx, "beta", 5); trend != TrendStable {
				t.Errorf("beta trend = %s, want %s", trend, TrendStable)
			}

			// An unmeasured source is empty, not an error.
			recs, err = store.ListSourceYield(ctx, "never-measured", 5)
			if err != nil {
				t.Errorf("unknown source should not error: %v", err)
			}
			if len(recs) != 0 {
				t.Errorf("unknown source should have no records, got %d", len(recs))
			}
			if trend, _, _ := store.SourceYieldTrend(ctx, "never-measured", 5); trend != TrendInsufficient {
				t.Errorf("unknown source trend = %s, want %s", trend, TrendInsufficient)
			}

			// A record with no source name is a programming error, not data.
			if err := store.SaveSourceYield(ctx, SourceYieldRecord{Sampled: 10, Alive: 1}); err == nil {
				t.Error("a record without a source name must be rejected")
			}
		})
	}
}

// History is pruned two ways, and either alone lets the other grow without
// bound: age-based pruning never fires for a source measured every few minutes,
// and the count cap never fires for one measured twice a year.
func TestSourceYieldPrunesByAgeAndCount(t *testing.T) {
	backends := map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemoryStore() },
		"redis": func(t *testing.T) Store {
			s, _ := newFakeRedisStore(t)
			return s
		},
	}
	for name, open := range backends {
		t.Run(name+"/age", func(t *testing.T) {
			store := open(t)
			ctx := context.Background()
			now := time.Now().UTC()

			// One measurement well past retention, one inside it.
			for _, at := range []time.Time{
				now.Add(-yieldRetention - 48*time.Hour),
				now.Add(-time.Hour),
			} {
				if err := store.SaveSourceYield(ctx, SourceYieldRecord{
					Source: "aged", MeasureAt: at, Sampled: 10, Alive: 2,
				}); err != nil {
					t.Fatal(err)
				}
			}
			// The newest write carries the cutoff, so save it last.
			if err := store.SaveSourceYield(ctx, SourceYieldRecord{
				Source: "aged", MeasureAt: now, Sampled: 10, Alive: 3,
			}); err != nil {
				t.Fatal(err)
			}

			recs, err := store.ListSourceYield(ctx, "aged", 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range recs {
				if r.MeasureAt.Before(now.Add(-yieldRetention)) {
					t.Errorf("a measurement older than the %v retention survived: %v",
						yieldRetention, r.MeasureAt)
				}
			}
			if len(recs) != 2 {
				t.Errorf("expected the 2 in-retention records, got %d", len(recs))
			}
		})

		t.Run(name+"/count", func(t *testing.T) {
			store := open(t)
			ctx := context.Background()
			base := time.Now().UTC()

			over := yieldMaxPerSource + 25
			for i := 0; i < over; i++ {
				if err := store.SaveSourceYield(ctx, SourceYieldRecord{
					Source:    "chatty",
					MeasureAt: base.Add(time.Duration(i) * time.Second),
					Sampled:   10, Alive: i % 10,
				}); err != nil {
					t.Fatal(err)
				}
			}
			recs, err := store.ListSourceYield(ctx, "chatty", over)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) > yieldMaxPerSource {
				t.Errorf("history grew past the %d cap: %d records", yieldMaxPerSource, len(recs))
			}
			// Pruning must drop the oldest, not the newest: a trend reads the head.
			if len(recs) > 0 && recs[0].MeasureAt.Before(base.Add(time.Duration(over-1)*time.Second)) {
				t.Error("the newest measurement was pruned; the cap must drop the oldest")
			}
		})
	}
}

func aliveOf(recs []SourceYieldRecord) []int {
	out := make([]int, len(recs))
	for i, r := range recs {
		out[i] = r.Alive
	}
	return out
}

// Yield history must survive independently of the event ring's 50-entry cap:
// a trend needs its own retention, not whatever the event feed happens to keep.
func TestSourceYieldHistorySurvivesEventChurn(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	base := time.Now().UTC()

	for i := 0; i < 6; i++ {
		rec := SourceYieldRecord{
			Source:    "gamma",
			MeasureAt: base.Add(-time.Duration(5-i) * time.Hour),
			Fetched:   100,
			Sampled:   100,
			Alive:     10 + i*5,
		}
		if err := store.SaveSourceYield(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	// Ordinary panel activity, more than the ring holds.
	for i := 0; i < 60; i++ {
		if err := store.PushEvent(ctx, "scrape round"); err != nil {
			t.Fatal(err)
		}
	}

	trend, records, err := store.SourceYieldTrend(ctx, "gamma", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("event churn destroyed yield history: got %d of 6 records", len(records))
	}
	if trend != "IMPROVING" {
		t.Errorf("expected IMPROVING, got %s", trend)
	}
}
