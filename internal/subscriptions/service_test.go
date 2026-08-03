package subscriptions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
)

func TestShouldSyncSubscription(t *testing.T) {
	now := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-2 * time.Hour)

	cases := []struct {
		name string
		item models.Subscription
		want bool
	}{
		{
			name: "disabled subscription",
			item: models.Subscription{Enabled: false, SyncIntervalSec: 3600},
			want: false,
		},
		{
			name: "never synced",
			item: models.Subscription{Enabled: true, SyncIntervalSec: 3600},
			want: true,
		},
		{
			name: "not due yet",
			item: models.Subscription{Enabled: true, SyncIntervalSec: 3600, LastSyncAt: &recent},
			want: false,
		},
		{
			name: "due now",
			item: models.Subscription{Enabled: true, SyncIntervalSec: 3600, LastSyncAt: &old},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSyncSubscription(tc.item, now); got != tc.want {
				t.Fatalf("shouldSyncSubscription() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSyncNotModifiedPreservesExistingNodes(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:               "127.0.0.1",
		PanelPort:               7890,
		DataDir:                 tempDir,
		DBPath:                  filepath.Join(tempDir, "app.db"),
		RuntimeDir:              filepath.Join(tempDir, "runtime"),
		ProdConfigPath:          filepath.Join(tempDir, "runtime", "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(tempDir, "runtime", "mihomo-probe.yaml"),
		ProdControllerAddr:      "127.0.0.1:19090",
		ProbeControllerAddr:     "127.0.0.1:19091",
		ProbeMixedPort:          17891,
		DefaultControllerSecret: "secret-token",
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer store.Close()

	settingsSvc := settings.NewService(store, cfg)
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	lastModified := time.Now().UTC().Format(http.TimeFormat)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != "etag-123" {
			t.Fatalf("If-None-Match = %q, want %q", got, "etag-123")
		}
		if got := r.Header.Get("If-Modified-Since"); got != lastModified {
			t.Fatalf("If-Modified-Since = %q, want %q", got, lastModified)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	svc := NewService(store, settingsSvc, events.NewBroker())
	sub, err := svc.Create(ctx, UpsertRequest{
		Name:            "demo-subscription",
		URL:             server.URL,
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	now := time.Now().UTC()
	if _, err := store.DB.ExecContext(ctx, `UPDATE subscriptions
		SET etag = ?, last_modified = ?, last_sync_status = ?, last_sync_at = ?, updated_at = ?
		WHERE id = ?`,
		"etag-123", lastModified, "ok", now, now, sub.ID,
	); err != nil {
		t.Fatalf("prepare subscription metadata error = %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO subscription_nodes (
			subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'available', ?, ?)`,
		sub.ID, "persist-node", "ss", "example.com", 443, "ss://example", `{}`, now, now,
	); err != nil {
		t.Fatalf("insert existing subscription node error = %v", err)
	}

	outcome, err := svc.Sync(ctx, sub.ID)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if outcome.Status != "not_modified" {
		t.Fatalf("Sync() status = %q, want %q", outcome.Status, "not_modified")
	}
	if outcome.Modified {
		t.Fatalf("Sync() modified = true, want false")
	}
	if outcome.CreatedCount != 0 || outcome.FailedCount != 0 {
		t.Fatalf("unexpected sync outcome: %+v", outcome)
	}

	updated, err := svc.Get(ctx, sub.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if updated.LastSyncStatus != "not_modified" {
		t.Fatalf("LastSyncStatus = %q, want %q", updated.LastSyncStatus, "not_modified")
	}
	if updated.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.LastError)
	}
	if updated.LastSyncAt == nil {
		t.Fatalf("LastSyncAt should be set after 304 sync")
	}

	nodes, err := svc.ListNodes(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes() len = %d, want 1", len(nodes))
	}
	if nodes[0].DisplayName != "persist-node" {
		t.Fatalf("preserved node name = %q, want %q", nodes[0].DisplayName, "persist-node")
	}
}

func TestAddAfterSyncHookRunsAllHooks(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:               "127.0.0.1",
		PanelPort:               7890,
		DataDir:                 tempDir,
		DBPath:                  filepath.Join(tempDir, "app.db"),
		RuntimeDir:              filepath.Join(tempDir, "runtime"),
		ProdConfigPath:          filepath.Join(tempDir, "runtime", "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(tempDir, "runtime", "mihomo-probe.yaml"),
		ProdControllerAddr:      "127.0.0.1:19090",
		ProbeControllerAddr:     "127.0.0.1:19091",
		ProbeMixedPort:          17891,
		DefaultControllerSecret: "secret-token",
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer store.Close()

	settingsSvc := settings.NewService(store, cfg)
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(`proxies:
  - name: demo-node
    type: trojan
    server: demo.example.com
    port: 443
    password: secret
`))
	}))
	defer server.Close()

	svc := NewService(store, settingsSvc, events.NewBroker())
	sub, err := svc.Create(ctx, UpsertRequest{
		Name:            "hook-demo",
		URL:             server.URL,
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	type hookResult struct {
		name           string
		subscriptionID int64
		nodeCount      int
	}
	results := make(chan hookResult, 2)
	svc.AddAfterSyncHook(func(_ context.Context, subscriptionID int64, nodeIDs []int64) {
		results <- hookResult{name: "hook-1", subscriptionID: subscriptionID, nodeCount: len(nodeIDs)}
	})
	svc.AddAfterSyncHook(func(_ context.Context, subscriptionID int64, nodeIDs []int64) {
		results <- hookResult{name: "hook-2", subscriptionID: subscriptionID, nodeCount: len(nodeIDs)}
	})

	outcome, err := svc.Sync(ctx, sub.ID)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !outcome.Modified || outcome.CreatedCount != 1 {
		t.Fatalf("unexpected sync outcome: %+v", outcome)
	}

	received := map[string]hookResult{}
	for range 2 {
		select {
		case item := <-results:
			received[item.name] = item
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for after-sync hooks")
		}
	}

	for _, hookName := range []string{"hook-1", "hook-2"} {
		item, ok := received[hookName]
		if !ok {
			t.Fatalf("missing hook result for %s", hookName)
		}
		if item.subscriptionID != sub.ID {
			t.Fatalf("%s subscriptionID = %d, want %d", hookName, item.subscriptionID, sub.ID)
		}
		if item.nodeCount != 1 {
			t.Fatalf("%s nodeCount = %d, want 1", hookName, item.nodeCount)
		}
	}
}

func TestListWithStatsAggregatesSubscriptionNodes(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:               "127.0.0.1",
		PanelPort:               7890,
		DataDir:                 tempDir,
		DBPath:                  filepath.Join(tempDir, "app.db"),
		RuntimeDir:              filepath.Join(tempDir, "runtime"),
		ProdConfigPath:          filepath.Join(tempDir, "runtime", "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(tempDir, "runtime", "mihomo-probe.yaml"),
		ProdControllerAddr:      "127.0.0.1:19090",
		ProbeControllerAddr:     "127.0.0.1:19091",
		ProbeMixedPort:          17891,
		DefaultControllerSecret: "secret-token",
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer store.Close()

	settingsSvc := settings.NewService(store, cfg)
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	svc := NewService(store, settingsSvc, events.NewBroker())
	sub, err := svc.Create(ctx, UpsertRequest{
		Name:            "stats-demo",
		URL:             "https://example.com/subscription.yaml",
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	now := time.Now().UTC()
	type nodeSeed struct {
		name      string
		enabled   int
		status    string
		latencyMS *int64
	}
	latencyFast := int64(120)
	latencySlow := int64(240)
	seeds := []nodeSeed{
		{name: "fast", enabled: 1, status: "available", latencyMS: &latencyFast},
		{name: "disabled-but-available", enabled: 0, status: "available", latencyMS: &latencySlow},
		{name: "offline", enabled: 1, status: "unavailable", latencyMS: nil},
	}
	for _, seed := range seeds {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO subscription_nodes (
				subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled,
				last_latency_ms, last_status, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sub.ID, seed.name, "ss", "example.com", 443, "payload", `{}`, seed.enabled,
			seed.latencyMS, seed.status, now, now,
		); err != nil {
			t.Fatalf("insert subscription_nodes seed %q error = %v", seed.name, err)
		}
	}

	items, err := svc.ListWithStats(ctx)
	if err != nil {
		t.Fatalf("ListWithStats() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ListWithStats() len = %d, want 1", len(items))
	}

	item := items[0]
	if item.ID != sub.ID {
		t.Fatalf("item.ID = %d, want %d", item.ID, sub.ID)
	}
	if item.TotalNodes != 3 {
		t.Fatalf("TotalNodes = %d, want 3", item.TotalNodes)
	}
	if item.AvailableNodes != 2 {
		t.Fatalf("AvailableNodes = %d, want 2", item.AvailableNodes)
	}
	if item.InvalidNodes != 2 {
		t.Fatalf("InvalidNodes = %d, want 2", item.InvalidNodes)
	}
	if item.AverageLatencyMS == nil {
		t.Fatalf("AverageLatencyMS should be set")
	}
	if *item.AverageLatencyMS != 180 {
		t.Fatalf("AverageLatencyMS = %d, want 180", *item.AverageLatencyMS)
	}
}

func TestCreateValidatesHeadersJSON(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	_, err := svc.Create(ctx, UpsertRequest{
		Name:            "invalid-headers",
		URL:             "https://example.com/subscription.yaml",
		HeadersJSON:     `{"User-Agent":1}`,
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err == nil {
		t.Fatalf("Create() error = nil, want headers_json validation error")
	}
}

func TestUpdateValidatesHeadersJSONAndUsesDefaultSyncInterval(t *testing.T) {
	ctx, settingsSvc, svc := newSubscriptionTestService(t)

	sub, err := svc.Create(ctx, UpsertRequest{
		Name:            "update-demo",
		URL:             "https://example.com/subscription.yaml",
		HeadersJSON:     `{"User-Agent":"mihomo"}`,
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := svc.Update(ctx, sub.ID, UpsertRequest{
		Name:            "update-demo",
		URL:             "https://example.com/subscription.yaml",
		HeadersJSON:     `{"User-Agent":1}`,
		Enabled:         true,
		SyncIntervalSec: 3600,
	}); err == nil {
		t.Fatalf("Update() error = nil, want headers_json validation error")
	}

	currentSettings, err := settingsSvc.Get(ctx)
	if err != nil {
		t.Fatalf("settingsSvc.Get() error = %v", err)
	}

	updated, err := svc.Update(ctx, sub.ID, UpsertRequest{
		Name:            "update-demo",
		URL:             "https://example.com/subscription.yaml",
		HeadersJSON:     "{\n  \"Authorization\": \"Bearer token\"\n}",
		Enabled:         true,
		SyncIntervalSec: 0,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.SyncIntervalSec != currentSettings.DefaultSubscriptionIntervalSec {
		t.Fatalf("SyncIntervalSec = %d, want %d", updated.SyncIntervalSec, currentSettings.DefaultSubscriptionIntervalSec)
	}
	if updated.HeadersJSON != `{"Authorization":"Bearer token"}` {
		t.Fatalf("HeadersJSON = %q, want canonical JSON", updated.HeadersJSON)
	}
}

func TestSyncPreservesUnchangedNodeIDAndState(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	rawNode := "trojan://password@demo.example.com:443#demo-node"
	parsedNode, err := nodes.ParseNodeURI(rawNode)
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(rawNode))
	}))
	defer server.Close()

	sub, err := svc.Create(ctx, UpsertRequest{
		Name:            "preserve-demo",
		URL:             server.URL,
		Enabled:         true,
		SyncIntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	now := time.Now().UTC()
	res, err := svc.store.DB.ExecContext(ctx, `INSERT INTO subscription_nodes (
			subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled,
			last_latency_ms, last_status, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, parsedNode.DisplayName, parsedNode.Protocol, parsedNode.Server, parsedNode.Port, parsedNode.RawPayload,
		nodes.NormalizeJSON(parsedNode.Normalized), 0, 123, "available", "keep-me", now, now,
	)
	if err != nil {
		t.Fatalf("insert existing subscription node error = %v", err)
	}
	existingID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}
	if _, err := svc.store.DB.ExecContext(ctx, `INSERT INTO subscription_nodes (
			subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'unknown', ?, ?)`,
		sub.ID, "stale-node", "trojan", "stale.example.com", 443, "trojan://password@stale.example.com:443#stale-node",
		`{"name":"stale-node","type":"trojan","server":"stale.example.com","port":443,"password":"password"}`, now, now,
	); err != nil {
		t.Fatalf("insert stale subscription node error = %v", err)
	}

	hookResults := make(chan []int64, 1)
	svc.SetAfterSyncHook(func(_ context.Context, _ int64, nodeIDs []int64) {
		hookResults <- append([]int64(nil), nodeIDs...)
	})

	outcome, err := svc.Sync(ctx, sub.ID)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !outcome.Modified || outcome.CreatedCount != 1 {
		t.Fatalf("unexpected sync outcome: %+v", outcome)
	}

	nodesAfterSync, err := svc.ListNodes(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(nodesAfterSync) != 1 {
		t.Fatalf("ListNodes() len = %d, want 1", len(nodesAfterSync))
	}
	node := nodesAfterSync[0]
	if node.ID != existingID {
		t.Fatalf("node.ID = %d, want preserved ID %d", node.ID, existingID)
	}
	if node.Enabled {
		t.Fatalf("node.Enabled = true, want preserved disabled state")
	}
	if node.LastStatus != "available" {
		t.Fatalf("node.LastStatus = %q, want %q", node.LastStatus, "available")
	}
	if node.LastError != "keep-me" {
		t.Fatalf("node.LastError = %q, want %q", node.LastError, "keep-me")
	}
	if node.LastLatencyMS == nil || *node.LastLatencyMS != 123 {
		t.Fatalf("node.LastLatencyMS = %v, want 123", node.LastLatencyMS)
	}

	select {
	case nodeIDs := <-hookResults:
		if len(nodeIDs) != 1 || nodeIDs[0] != existingID {
			t.Fatalf("after-sync nodeIDs = %v, want [%d]", nodeIDs, existingID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for after-sync hook")
	}
}

func newSubscriptionTestService(t *testing.T) (context.Context, *settings.Service, *Service) {
	t.Helper()

	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:               "127.0.0.1",
		PanelPort:               7890,
		DataDir:                 tempDir,
		DBPath:                  filepath.Join(tempDir, "app.db"),
		RuntimeDir:              filepath.Join(tempDir, "runtime"),
		ProdConfigPath:          filepath.Join(tempDir, "runtime", "mihomo-prod.yaml"),
		ProbeConfigPath:         filepath.Join(tempDir, "runtime", "mihomo-probe.yaml"),
		ProdControllerAddr:      "127.0.0.1:19090",
		ProbeControllerAddr:     "127.0.0.1:19091",
		ProbeMixedPort:          17891,
		DefaultControllerSecret: "secret-token",
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	settingsSvc := settings.NewService(store, cfg)
	hash, err := auth.HashPassword("admin")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	svc := NewService(store, settingsSvc, events.NewBroker())
	return ctx, settingsSvc, svc
}
