package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenFreshDBDoesNotCreateDeprecatedSettingsColumns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	columns, err := tableColumns(store.DB, "settings")
	if err != nil {
		t.Fatalf("tableColumns(settings) error = %v", err)
	}
	if columns["pool_port_min"] || columns["pool_port_max"] {
		t.Fatalf("unexpected deprecated settings columns: %#v", columns)
	}
}

func TestOpenMigratesLegacyProxyPoolsWithForeignKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	now := time.Now().UTC()
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE proxy_pools (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			listen_host TEXT NOT NULL,
			listen_port INTEGER NOT NULL,
			auth_enabled INTEGER NOT NULL DEFAULT 0,
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
		`CREATE UNIQUE INDEX idx_proxy_pools_listen_port ON proxy_pools(listen_port)`,
		`CREATE TABLE proxy_pool_members (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pool_id INTEGER NOT NULL,
			source_type TEXT NOT NULL,
			source_node_id INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			weight INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(pool_id) REFERENCES proxy_pools(id) ON DELETE CASCADE
		)`,
	}
	for _, stmt := range statements {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}
	}
	if _, err := legacy.Exec(
		`INSERT INTO proxy_pools (
			id, name, protocol, listen_host, listen_port, auth_enabled, auth_username, auth_password_secret,
			strategy, failover_enabled, enabled, last_publish_status, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "demo", "http", "0.0.0.0", 18080, 1, "user", "pass",
		"round_robin", 1, 1, "published", "", now, now,
	); err != nil {
		t.Fatalf("insert legacy proxy_pools row: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO proxy_pool_members (
			pool_id, source_type, source_node_id, enabled, weight, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		1, "manual", 10, 1, 1, now, now,
	); err != nil {
		t.Fatalf("insert legacy proxy_pool_members row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() legacy db error = %v", err)
	}
	defer store.Close()

	columns, err := tableColumns(store.DB, "proxy_pools")
	if err != nil {
		t.Fatalf("tableColumns(proxy_pools) error = %v", err)
	}
	for _, legacyColumn := range []string{"protocol", "listen_host", "listen_port", "auth_enabled"} {
		if columns[legacyColumn] {
			t.Fatalf("expected legacy column %q to be removed", legacyColumn)
		}
	}

	var authUsername string
	if err := store.DB.QueryRowContext(context.Background(), `SELECT auth_username FROM proxy_pools WHERE id = 1`).Scan(&authUsername); err != nil {
		t.Fatalf("query migrated proxy_pools row: %v", err)
	}
	if authUsername != "user" {
		t.Fatalf("expected migrated auth_username to be preserved, got %q", authUsername)
	}

	var memberCount int
	if err := store.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM proxy_pool_members WHERE pool_id = 1`).Scan(&memberCount); err != nil {
		t.Fatalf("query migrated proxy_pool_members rows: %v", err)
	}
	if memberCount != 1 {
		t.Fatalf("expected migrated proxy_pool_members row to be preserved, got %d", memberCount)
	}
}

func TestOpenAppliesConnectionPragmasToEveryConnection(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	conn1, err := store.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("DB.Conn() conn1 error = %v", err)
	}
	defer conn1.Close()

	conn2, err := store.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("DB.Conn() conn2 error = %v", err)
	}
	defer conn2.Close()

	for _, conn := range []*sql.Conn{conn1, conn2} {
		var busyTimeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("PRAGMA busy_timeout error = %v", err)
		}
		if busyTimeout != sqliteBusyTimeoutMS {
			t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMS)
		}

		var foreignKeys int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("PRAGMA foreign_keys error = %v", err)
		}
		if foreignKeys != 1 {
			t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
		}

		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("PRAGMA journal_mode error = %v", err)
		}
		if journalMode != "wal" {
			t.Fatalf("journal_mode = %q, want %q", journalMode, "wal")
		}
	}
}

func tableColumns(database *sql.DB, table string) (map[string]bool, error) {
	rows, err := database.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
