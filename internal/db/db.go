package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

const (
	sqliteBusyTimeoutMS = 5000
	sqliteMaxOpenConns  = 4
	sqliteMaxIdleConns  = 4
)

func Open(path string) (*Store, error) {
	database, err := sql.Open("sqlite",
		fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path, sqliteBusyTimeoutMS),
	)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(sqliteMaxOpenConns)
	database.SetMaxIdleConns(sqliteMaxIdleConns)
	database.SetConnMaxLifetime(30 * time.Minute)

	store := &Store{DB: database}
	if err := store.migrate(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) ExecContext(ctx context.Context, query string, args ...any) error {
	_, err := s.DB.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			panel_host TEXT NOT NULL,
			panel_port INTEGER NOT NULL,
			password_hash TEXT NOT NULL,
			speed_test_enabled INTEGER NOT NULL DEFAULT 0,
			latency_test_url TEXT NOT NULL,
			speed_test_url TEXT NOT NULL,
			latency_timeout_ms INTEGER NOT NULL,
			speed_timeout_ms INTEGER NOT NULL,
			latency_concurrency INTEGER NOT NULL,
			speed_concurrency INTEGER NOT NULL,
			default_subscription_interval_sec INTEGER NOT NULL,
			mihomo_controller_secret TEXT NOT NULL,
			failure_retry_count INTEGER NOT NULL DEFAULT 2,
			log_level TEXT NOT NULL DEFAULT 'info',
			speed_max_bytes INTEGER NOT NULL DEFAULT 5000000,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS subscriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			headers_json TEXT NOT NULL DEFAULT '{}',
			fetch_proxy TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			sync_interval_sec INTEGER NOT NULL,
			last_sync_at TIMESTAMP NULL,
			last_sync_status TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			etag TEXT NOT NULL DEFAULT '',
			last_modified TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS subscription_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			subscription_id INTEGER NOT NULL,
			display_name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			server TEXT NOT NULL,
			port INTEGER NOT NULL,
			raw_payload TEXT NOT NULL,
			normalized_json TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_latency_ms INTEGER NULL,
			last_speed_mbps REAL NULL,
			last_status TEXT NOT NULL DEFAULT 'unknown',
			last_test_at TIMESTAMP NULL,
			last_speed_at TIMESTAMP NULL,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS manual_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			display_name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			server TEXT NOT NULL,
			port INTEGER NOT NULL,
			raw_payload TEXT NOT NULL,
			normalized_json TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_latency_ms INTEGER NULL,
			last_speed_mbps REAL NULL,
			last_status TEXT NOT NULL DEFAULT 'unknown',
			last_test_at TIMESTAMP NULL,
			last_speed_at TIMESTAMP NULL,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS proxy_pools (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			auth_username TEXT NOT NULL DEFAULT '',
			auth_password_secret TEXT NOT NULL DEFAULT '',
			strategy TEXT NOT NULL,
			failover_enabled INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_published_at TIMESTAMP NULL,
			last_publish_status TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS proxy_pool_members (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pool_id INTEGER NOT NULL,
			source_type TEXT NOT NULL,
			source_node_id INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			weight INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(pool_id) REFERENCES proxy_pools(id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_pool_member_unique
			ON proxy_pool_members(pool_id, source_type, source_node_id);`,
		`CREATE TABLE IF NOT EXISTS probe_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_type TEXT NOT NULL,
			source_node_id INTEGER NOT NULL,
			test_type TEXT NOT NULL,
			success INTEGER NOT NULL,
			latency_ms INTEGER NULL,
			speed_mbps REAL NULL,
			error_message TEXT NOT NULL DEFAULT '',
			tested_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			token TEXT PRIMARY KEY,
			expires_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS custom_scrapers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			urls_json TEXT NOT NULL DEFAULT '[]',
			format TEXT NOT NULL DEFAULT 'plaintext',
			protocol TEXT NOT NULL DEFAULT 'http',
			enabled INTEGER NOT NULL DEFAULT 1,
			fragile INTEGER NOT NULL DEFAULT 0,
			parse_options_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS traffic_stats (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			up_bytes INTEGER NOT NULL DEFAULT 0,
			down_bytes INTEGER NOT NULL DEFAULT 0,
			connections INTEGER NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			fail INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL
		);`,
	}

	for _, stmt := range statements {
		if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	// Ensure auth_username column exists for upgraded databases
	if err := s.ensureColumn(ctx, "proxy_pools", "auth_username", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_pools", "auth_password_secret", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Migrate legacy proxy_pools table: drop old columns by recreating the table
	if err := s.migrateProxyPoolsDropLegacy(ctx); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_pools", "strategy_label", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_pools", "strategy_advanced_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	// Free-proxy / session runtime settings
	if err := s.ensureColumn(ctx, "settings", "session_max_age_sec", "INTEGER NOT NULL DEFAULT 604800"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "scrape_interval_sec", "INTEGER NOT NULL DEFAULT 300"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "validate_interval_sec", "INTEGER NOT NULL DEFAULT 120"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "free_validate_url", "TEXT NOT NULL DEFAULT 'https://www.gstatic.com/generate_204'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "free_validate_timeout_ms", "INTEGER NOT NULL DEFAULT 8000"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "free_validate_concurrency", "INTEGER NOT NULL DEFAULT 32"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "proxy_chain_hops", "INTEGER NOT NULL DEFAULT 2"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "settings", "feature_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_pools", "channel", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "subscriptions", "fetch_proxy", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// F2–F6 tables
	extra := []string{
		`CREATE TABLE IF NOT EXISTS proxy_blacklist (
			host TEXT PRIMARY KEY,
			reason TEXT NOT NULL DEFAULT '',
			until_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT 'proxies:read',
			created_at TIMESTAMP NOT NULL,
			last_used_at TIMESTAMP NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TIMESTAMP NOT NULL,
			actor TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			detail_json TEXT NOT NULL DEFAULT '{}',
			ip TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS traffic_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TIMESTAMP NOT NULL,
			up_bytes INTEGER NOT NULL DEFAULT 0,
			down_bytes INTEGER NOT NULL DEFAULT 0,
			active_in INTEGER NOT NULL DEFAULT 0,
			active_out INTEGER NOT NULL DEFAULT 0,
			active_conns INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_samples_ts ON traffic_samples(ts);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_at ON audit_logs(at);`,
		// Per-channel temporary bans. Persisted so a restart does not release
		// every sidelined proxy at once; the sliding-window counters behind them
		// are deliberately not persisted (stale evidence must not ban).
		`CREATE TABLE IF NOT EXISTS channel_bans (
			channel TEXT NOT NULL,
			addr TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			banned_at TIMESTAMP NOT NULL,
			until_at TIMESTAMP NOT NULL,
			strikes INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (channel, addr)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_channel_bans_until ON channel_bans(until_at);`,
		// Recent request outcomes for the channel panel. Capped by retention, not
		// by a primary key — this is a log, not a current-state table.
		`CREATE TABLE IF NOT EXISTS channel_outcomes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TIMESTAMP NOT NULL,
			channel TEXT NOT NULL,
			addr TEXT NOT NULL,
			ok INTEGER NOT NULL DEFAULT 0,
			status INTEGER NOT NULL DEFAULT 0,
			err TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			reported INTEGER NOT NULL DEFAULT 0,
			banned INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_channel_outcomes_at ON channel_outcomes(at);`,
		`CREATE INDEX IF NOT EXISTS idx_channel_outcomes_channel ON channel_outcomes(channel, at);`,
		// Addresses (optionally scoped to a channel) that automatic rules must not
		// ban. Empty channel = never auto-ban this addr anywhere.
		`CREATE TABLE IF NOT EXISTS channel_allowlist (
			channel TEXT NOT NULL,
			addr TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (channel, addr)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_probe_history_tested ON probe_history(tested_at);`,
		`CREATE INDEX IF NOT EXISTS idx_probe_history_node ON probe_history(source_type, source_node_id, tested_at);`,
		`CREATE INDEX IF NOT EXISTS idx_subscription_nodes_sub ON subscription_nodes(subscription_id, last_status);`,
		`CREATE TABLE IF NOT EXISTS free_proxy_snapshot (
			addr TEXT PRIMARY KEY,
			body_json TEXT NOT NULL,
			in_raw INTEGER NOT NULL DEFAULT 0,
			in_scored INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS scraper_toggles (
			name TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS source_stats (
			name TEXT PRIMARY KEY,
			ok INTEGER NOT NULL DEFAULT 0,
			fail INTEGER NOT NULL DEFAULT 0,
			latency_sum_ms INTEGER NOT NULL DEFAULT 0,
			auto_disabled INTEGER NOT NULL DEFAULT 0,
			disabled_until TIMESTAMP NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS validate_batches (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ok INTEGER NOT NULL,
			fail INTEGER NOT NULL,
			raw_n INTEGER NOT NULL DEFAULT 0,
			recheck_n INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			at TIMESTAMP NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_validate_batches_at ON validate_batches(at);`,
		`CREATE TABLE IF NOT EXISTS channel_rules (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			statuses TEXT NOT NULL DEFAULT '',
			threshold INTEGER NOT NULL DEFAULT 0,
			rate REAL NOT NULL DEFAULT 0,
			min_samples INTEGER NOT NULL DEFAULT 0,
			match TEXT NOT NULL DEFAULT '',
			ttl_sec INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL
		);`,
	}
	for _, stmt := range extra {
		if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("feature migration: %w", err)
		}
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM probe_history WHERE tested_at < datetime('now', '-14 days')`); err != nil {
		return fmt.Errorf("prune probe_history: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM validate_batches WHERE id NOT IN (SELECT id FROM (SELECT id FROM validate_batches ORDER BY id DESC LIMIT 200))`); err != nil {
		return fmt.Errorf("prune validate_batches: %w", err)
	}
	return nil
}

// migrateProxyPoolsDropLegacy drops legacy columns (protocol, listen_host, listen_port, auth_enabled)
// from the proxy_pools table if they still exist. Uses SQLite table recreation pattern.
func (s *Store) migrateProxyPoolsDropLegacy(ctx context.Context) error {
	if !s.hasColumn(ctx, "proxy_pools", "listen_port") {
		return nil // already migrated
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("proxy_pools migration: acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("proxy_pools migration: disable foreign keys: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("proxy_pools migration: begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		`DROP TABLE IF EXISTS proxy_pools_new`,
		`CREATE TABLE proxy_pools_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			auth_username TEXT NOT NULL DEFAULT '',
			auth_password_secret TEXT NOT NULL DEFAULT '',
			strategy TEXT NOT NULL,
			failover_enabled INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_published_at TIMESTAMP NULL,
			last_publish_status TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`INSERT INTO proxy_pools_new (id, name, auth_username, auth_password_secret, strategy,
			failover_enabled, enabled, last_published_at, last_publish_status, last_error, created_at, updated_at)
		SELECT id, name, auth_username, auth_password_secret, strategy,
			failover_enabled, enabled, last_published_at, last_publish_status, last_error, created_at, updated_at
		FROM proxy_pools`,
		`DROP INDEX IF EXISTS idx_proxy_pools_listen_port`,
		`DROP TABLE proxy_pools`,
		`ALTER TABLE proxy_pools_new RENAME TO proxy_pools`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("proxy_pools migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("proxy_pools migration: commit: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("proxy_pools migration: re-enable foreign keys: %w", err)
	}
	return nil
}

func (s *Store) hasColumn(ctx context.Context, table, column string) bool {
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan table %s info: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table %s info: %w", table, err)
	}

	if _, err := s.DB.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
