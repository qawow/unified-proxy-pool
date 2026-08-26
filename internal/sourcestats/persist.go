package sourcestats

import (
	"context"
	"database/sql"
	"log"
	"time"

	"unified-proxy-pool/internal/db"
)

func (r *Registry) Attach(store *db.Store) {
	if r == nil || store == nil {
		return
	}
	r.mu.Lock()
	r.db = store
	r.mu.Unlock()
	if err := r.load(context.Background()); err != nil {
		log.Printf("source_stats load: %v", err)
	}
	go r.flushLoop()
}

func (r *Registry) load(ctx context.Context) error {
	rows, err := r.db.LoadSourceStats(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range rows {
		st := &Stat{
			Name:         row.Name,
			OK:           row.OK,
			Fail:         row.Fail,
			LatencySumMS: row.LatencySumMS,
			AutoDisabled: row.AutoDisabled,
		}
		if row.DisabledUntil.Valid {
			st.DisabledUntil = row.DisabledUntil.Time
		}
		total := st.OK + st.Fail
		if total > 0 {
			st.SuccessRate = float64(st.OK) / float64(total)
		}
		if st.OK > 0 {
			st.AvgLatencyMS = float64(st.LatencySumMS) / float64(st.OK)
		}
		r.m[st.Name] = st
	}
	return nil
}

func (r *Registry) markDirty() {
	r.dirty.Store(true)
}

func (r *Registry) flushLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		r.flush()
	}
}

func (r *Registry) Flush() { r.flush() }

func (r *Registry) flush() {
	if r.db == nil || !r.dirty.Swap(false) {
		return
	}
	stats := r.List()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, st := range stats {
		row := db.SourceStatRow{
			Name: st.Name, OK: st.OK, Fail: st.Fail, LatencySumMS: st.LatencySumMS,
			AutoDisabled: st.AutoDisabled,
		}
		if !st.DisabledUntil.IsZero() {
			row.DisabledUntil = sql.NullTime{Time: st.DisabledUntil, Valid: true}
		}
		if err := r.db.UpsertSourceStat(ctx, row); err != nil {
			log.Printf("source_stats save %s: %v", st.Name, err)
			r.dirty.Store(true)
		}
	}
}
