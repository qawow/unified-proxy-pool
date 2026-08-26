package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSourceStatsAndValidateBatchPersist(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	if err := store.UpsertSourceStat(ctx, SourceStatRow{Name: "a", OK: 3, Fail: 1, LatencySumMS: 300}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := store.LoadSourceStats(ctx)
	if err != nil || len(rows) != 1 || rows[0].OK != 3 {
		t.Fatalf("load stats: %v %+v", err, rows)
	}

	if err := store.InsertValidateBatch(ctx, 10, 2, 8, 4, 1500); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	ok, fail, raw, recheck, dur, at, err := store.LatestValidateBatch(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if ok != 10 || fail != 2 || raw != 8 || recheck != 4 || dur != 1500 || at.IsZero() {
		t.Fatalf("latest mismatch %d %d %d %d %d %v", ok, fail, raw, recheck, dur, at)
	}
	sumOK, sumFail, n, err := store.SumValidateBatches(ctx)
	if err != nil || sumOK != 10 || sumFail != 2 || n != 1 {
		t.Fatalf("sum: %v %d %d %d", err, sumOK, sumFail, n)
	}
}
