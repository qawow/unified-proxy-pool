package db

import (
	"context"
	"database/sql"
	"time"
)

type FreeProxyRow struct {
	Addr     string
	JSON     string
	InRaw    bool
	InScored bool
	InRetry  bool
}

func (s *Store) ReplaceFreeProxySnapshot(ctx context.Context, rows []FreeProxyRow, toggles map[string]bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM free_proxy_snapshot`); err != nil {
		return err
	}
	now := time.Now().UTC()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO free_proxy_snapshot(addr, body_json, in_raw, in_scored, updated_at) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		raw, scored := 0, 0
		if row.InRaw {
			raw = 1
		}
		if row.InScored {
			scored = 1
		}
		if _, err := stmt.ExecContext(ctx, row.Addr, row.JSON, raw, scored, now); err != nil {
			return err
		}
	}
	if toggles != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM scraper_toggles`); err != nil {
			return err
		}
		tstmt, err := tx.PrepareContext(ctx, `INSERT INTO scraper_toggles(name, enabled, updated_at) VALUES(?,?,?)`)
		if err != nil {
			return err
		}
		defer tstmt.Close()
		for name, on := range toggles {
			en := 0
			if on {
				en = 1
			}
			if _, err := tstmt.ExecContext(ctx, name, en, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) LoadFreeProxySnapshot(ctx context.Context) ([]FreeProxyRow, map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT addr, body_json, in_raw, in_scored FROM free_proxy_snapshot`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []FreeProxyRow
	for rows.Next() {
		var r FreeProxyRow
		var raw, scored int
		if err := rows.Scan(&r.Addr, &r.JSON, &raw, &scored); err != nil {
			return nil, nil, err
		}
		r.InRaw = raw == 1
		r.InScored = scored == 1
		out = append(out, r)
	}
	trows, err := s.DB.QueryContext(ctx, `SELECT name, enabled FROM scraper_toggles`)
	if err != nil {
		return out, nil, rows.Err()
	}
	defer trows.Close()
	toggles := map[string]bool{}
	for trows.Next() {
		var name string
		var en int
		if err := trows.Scan(&name, &en); err != nil {
			return out, toggles, err
		}
		toggles[name] = en == 1
	}
	return out, toggles, trows.Err()
}

type SourceStatRow struct {
	Name          string
	OK            int64
	Fail          int64
	LatencySumMS  int64
	AutoDisabled  bool
	DisabledUntil sql.NullTime
}

func (s *Store) UpsertSourceStat(ctx context.Context, row SourceStatRow) error {
	en := 0
	if row.AutoDisabled {
		en = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO source_stats(name, ok, fail, latency_sum_ms, auto_disabled, disabled_until, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET ok=excluded.ok, fail=excluded.fail, latency_sum_ms=excluded.latency_sum_ms,
			auto_disabled=excluded.auto_disabled, disabled_until=excluded.disabled_until, updated_at=excluded.updated_at`,
		row.Name, row.OK, row.Fail, row.LatencySumMS, en, row.DisabledUntil, time.Now().UTC())
	return err
}

func (s *Store) LoadSourceStats(ctx context.Context) ([]SourceStatRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, ok, fail, latency_sum_ms, auto_disabled, disabled_until FROM source_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceStatRow
	for rows.Next() {
		var r SourceStatRow
		var en int
		if err := rows.Scan(&r.Name, &r.OK, &r.Fail, &r.LatencySumMS, &en, &r.DisabledUntil); err != nil {
			return nil, err
		}
		r.AutoDisabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) InsertValidateBatch(ctx context.Context, ok, fail, raw, recheck int, durationMS int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO validate_batches(ok, fail, raw_n, recheck_n, duration_ms, at) VALUES(?,?,?,?,?,?)`,
		ok, fail, raw, recheck, durationMS, time.Now().UTC())
	if err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM validate_batches WHERE id NOT IN (SELECT id FROM (SELECT id FROM validate_batches ORDER BY id DESC LIMIT 200))`)
	return nil
}

func (s *Store) LatestValidateBatch(ctx context.Context) (ok, fail, raw, recheck int, durationMS int64, at time.Time, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT ok, fail, raw_n, recheck_n, duration_ms, at FROM validate_batches ORDER BY id DESC LIMIT 1`).
		Scan(&ok, &fail, &raw, &recheck, &durationMS, &at)
	return
}

type ValidateBatchRow struct {
	OK, Fail, Raw, Recheck int
	DurationMS             int64
	At                     time.Time
}

func (s *Store) SumValidateBatches(ctx context.Context) (ok, fail, batches int64, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(ok),0), COALESCE(SUM(fail),0), COUNT(*) FROM validate_batches`).
		Scan(&ok, &fail, &batches)
	return
}

func (s *Store) ListValidateBatches(ctx context.Context, limit int) ([]ValidateBatchRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ok, fail, raw_n, recheck_n, duration_ms, at FROM validate_batches ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ValidateBatchRow
	for rows.Next() {
		var r ValidateBatchRow
		if err := rows.Scan(&r.OK, &r.Fail, &r.Raw, &r.Recheck, &r.DurationMS, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
