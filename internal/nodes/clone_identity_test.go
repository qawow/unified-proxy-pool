package nodes

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
)

func TestFreezeCloneIdentity(t *testing.T) {
	t.Run("implicit TLS+WS host frozen", func(t *testing.T) {
		cp := map[string]any{
			"type": "vless", "tls": true, "network": "ws",
			"ws-opts": map[string]any{"path": "/ray"},
		}
		freezeCloneIdentity(cp, "origin.example.com")
		if cp["servername"] != "origin.example.com" || cp["sni"] != "origin.example.com" {
			t.Fatalf("SNI not frozen: %+v", cp)
		}
		ws := cp["ws-opts"].(map[string]any)
		headers := ws["headers"].(map[string]any)
		if headers["Host"] != "origin.example.com" {
			t.Fatalf("WS Host not frozen: %+v", ws)
		}
	})

	t.Run("explicit overrides kept", func(t *testing.T) {
		cp := map[string]any{
			"type": "vless", "tls": true, "network": "ws",
			"servername": "cdn.example.com",
			"ws-opts": map[string]any{
				"headers": map[string]any{"Host": "cdn.example.com"},
			},
		}
		freezeCloneIdentity(cp, "origin.example.com")
		if cp["servername"] != "cdn.example.com" {
			t.Fatalf("explicit servername overwritten: %+v", cp)
		}
		headers := cp["ws-opts"].(map[string]any)["headers"].(map[string]any)
		if headers["Host"] != "cdn.example.com" {
			t.Fatalf("explicit Host overwritten: %+v", headers)
		}
	})

	t.Run("IP source adds nothing", func(t *testing.T) {
		cp := map[string]any{"type": "trojan"}
		freezeCloneIdentity(cp, "203.0.113.7")
		if _, ok := cp["servername"]; ok {
			t.Fatalf("SNI frozen from an IP source: %+v", cp)
		}
	})

	t.Run("non-TLS node untouched", func(t *testing.T) {
		cp := map[string]any{"type": "ss", "cipher": "aes-128-gcm"}
		freezeCloneIdentity(cp, "origin.example.com")
		if _, ok := cp["servername"]; ok {
			t.Fatalf("SNI frozen on a plaintext node: %+v", cp)
		}
	})

	t.Run("trojan TLS-by-default", func(t *testing.T) {
		cp := map[string]any{"type": "trojan", "password": "p"}
		freezeCloneIdentity(cp, "origin.example.com")
		if cp["sni"] != "origin.example.com" {
			t.Fatalf("trojan SNI not frozen: %+v", cp)
		}
	})
}

// The cloned row must carry the frozen identity in normalized_json: server is
// the CF IP while SNI/Host still name the origin hostname.
func TestCloneWithServersFreezesIdentity(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := NewService(store, events.NewBroker())
	ctx := context.Background()

	norm := map[string]any{
		"name": "cf-vless", "type": "vless", "server": "origin.example.com",
		"port": 443, "uuid": "u", "tls": true, "network": "ws",
		"ws-opts": map[string]any{"path": "/ray"},
	}
	body := NormalizeJSON(norm)
	now := time.Now().UTC()
	res, err := store.DB.ExecContext(ctx, `INSERT INTO manual_nodes (
		display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, 'unknown', ?, ?)`,
		"cf-vless", "vless", "origin.example.com", 443, body, body, now, now)
	if err != nil {
		t.Fatal(err)
	}
	srcID, _ := res.LastInsertId()

	n, err := s.CloneWithServers(ctx, srcID, []string{"104.21.5.6"})
	if err != nil || n != 1 {
		t.Fatalf("CloneWithServers: n=%d err=%v", n, err)
	}
	var clonedJSON string
	if err := store.DB.QueryRowContext(ctx,
		`SELECT normalized_json FROM manual_nodes WHERE server = ?`, "104.21.5.6").Scan(&clonedJSON); err != nil {
		t.Fatalf("cloned row: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(clonedJSON), &out); err != nil {
		t.Fatal(err)
	}
	if out["server"] != "104.21.5.6" {
		t.Fatalf("server = %v", out["server"])
	}
	if out["servername"] != "origin.example.com" || out["sni"] != "origin.example.com" {
		t.Fatalf("clone lost the TLS identity: %+v", out)
	}
	ws := out["ws-opts"].(map[string]any)
	if ws["headers"].(map[string]any)["Host"] != "origin.example.com" {
		t.Fatalf("clone lost the WS Host: %+v", ws)
	}
	// The source template must be untouched.
	var srcJSON string
	if err := store.DB.QueryRowContext(ctx,
		`SELECT normalized_json FROM manual_nodes WHERE id = ?`, srcID).Scan(&srcJSON); err != nil {
		t.Fatal(err)
	}
	var srcOut map[string]any
	_ = json.Unmarshal([]byte(srcJSON), &srcOut)
	if srcOut["server"] != "origin.example.com" {
		t.Fatalf("source template mutated: %+v", srcOut)
	}
}
