package traffichist

import (
	"context"
	"database/sql"
	"time"

	"unified-proxy-pool/internal/traffic"
)

type Sample struct {
	TS         time.Time `json:"ts"`
	UpBytes    int64     `json:"up_bytes"`
	DownBytes  int64     `json:"down_bytes"`
	ActiveIn   int64     `json:"active_in"`
	ActiveOut  int64     `json:"active_out"`
	ActiveConns int64    `json:"active_conns"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Start(ctx context.Context, every time.Duration, retainHours int) {
	if s == nil || s.db == nil {
		return
	}
	if every < 15*time.Second {
		every = 60 * time.Second
	}
	if retainHours < 1 {
		retainHours = 48
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				snap := traffic.Default.Snapshot()
				_, _ = s.db.ExecContext(ctx, `INSERT INTO traffic_samples(ts, up_bytes, down_bytes, active_in, active_out, active_conns) VALUES(?,?,?,?,?,?)`,
					time.Now().UTC(), snap.UpBytes, snap.DownBytes, snap.ActiveIn, snap.ActiveOut, snap.ActiveConns)
				cut := time.Now().UTC().Add(-time.Duration(retainHours) * time.Hour)
				_, _ = s.db.ExecContext(ctx, `DELETE FROM traffic_samples WHERE ts < ?`, cut)
			}
		}
	}()
}

func (s *Store) History(ctx context.Context, hours int) ([]Sample, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	rows, err := s.db.QueryContext(ctx, `SELECT ts, up_bytes, down_bytes, active_in, active_out, active_conns FROM traffic_samples WHERE ts >= ? ORDER BY ts ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var sm Sample
		if err := rows.Scan(&sm.TS, &sm.UpBytes, &sm.DownBytes, &sm.ActiveIn, &sm.ActiveOut, &sm.ActiveConns); err != nil {
			continue
		}
		out = append(out, sm)
	}
	return out, nil
}
