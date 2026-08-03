package pools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/models"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
)

func TestServicePublishWritesConfigs(t *testing.T) {
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

	broker := events.NewBroker()
	nodeSvc := nodes.NewService(store, broker)
	subSvc := subscriptions.NewService(store, settingsSvc, broker)
	mihomoMgr := mihomo.NewManager(mihomo.Options{
		BinaryPath:          "nonexistent-mihomo-binary",
		RuntimeDir:          cfg.RuntimeDir,
		ProdConfigPath:      cfg.ProdConfigPath,
		ProbeConfigPath:     cfg.ProbeConfigPath,
		ProdControllerAddr:  cfg.ProdControllerAddr,
		ProbeControllerAddr: cfg.ProbeControllerAddr,
		ProbeMixedPort:      cfg.ProbeMixedPort,
	})
	poolSvc := NewService(store, settingsSvc, nodeSvc, subSvc, mihomoMgr, broker)

	createdNodes, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@demo.example.com:443#demo-node",
	})
	if err != nil {
		t.Fatalf("Create manual node error = %v", err)
	}
	pool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "demo-pool",
		AuthUsername:       "testuser",
		AuthPasswordSecret: "testpass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create pool error = %v", err)
	}
	if err := poolSvc.UpdateMembers(ctx, pool.ID, []MemberInput{{
		SourceType:   "manual",
		SourceNodeID: createdNodes[0].ID,
		Enabled:      true,
		Weight:       1,
	}}); err != nil {
		t.Fatalf("UpdateMembers() error = %v", err)
	}
	if err := poolSvc.Publish(ctx, pool.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	prodConfig, err := os.ReadFile(cfg.ProdConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(prod) error = %v", err)
	}
	probeConfig, err := os.ReadFile(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(probe) error = %v", err)
	}

	if !strings.Contains(string(prodConfig), "pool-group-") || !strings.Contains(string(prodConfig), "listeners:") || !strings.Contains(string(prodConfig), "demo.example.com") || !strings.Contains(string(prodConfig), "log-level: info") {
		t.Fatalf("unexpected prod config:\n%s", string(prodConfig))
	}
	if !strings.Contains(string(probeConfig), "mixed-port: 17891") ||
		!strings.Contains(string(probeConfig), "demo.example.com") ||
		!strings.Contains(string(probeConfig), "SPEED_SLOT_1") ||
		!strings.Contains(string(probeConfig), "speed-slot-1") ||
		!strings.Contains(string(probeConfig), "port: 17892") ||
		!strings.Contains(string(probeConfig), "log-level: info") {
		t.Fatalf("unexpected probe config:\n%s", string(probeConfig))
	}
}

func TestPublishExpandsWeightedMembersForRoundRobin(t *testing.T) {
	ctx, cfg, _, nodeSvc, poolSvc := newPublishTestService(t)

	createdNodes, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@demo.example.com:443#demo-node",
	})
	if err != nil {
		t.Fatalf("Create manual node error = %v", err)
	}

	pool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "weighted-pool",
		AuthUsername:       "weighted-user",
		AuthPasswordSecret: "weighted-pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create pool error = %v", err)
	}
	if err := poolSvc.UpdateMembers(ctx, pool.ID, []MemberInput{{
		SourceType:   "manual",
		SourceNodeID: createdNodes[0].ID,
		Enabled:      true,
		Weight:       3,
	}}); err != nil {
		t.Fatalf("UpdateMembers() error = %v", err)
	}

	if err := poolSvc.Publish(ctx, pool.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	prodConfig, err := os.ReadFile(cfg.ProdConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(prod) error = %v", err)
	}

	proxyName := RuntimeNodeName(models.RuntimeNode{
		SourceType:   "manual",
		SourceNodeID: createdNodes[0].ID,
		DisplayName:  createdNodes[0].DisplayName,
	})
	if got := strings.Count(string(prodConfig), proxyName); got != 4 {
		t.Fatalf("expected weighted proxy %q to appear 4 times (1 proxy + 3 group entries), got %d\n%s", proxyName, got, string(prodConfig))
	}
	if !strings.Contains(string(prodConfig), fmt.Sprintf("strategy: %s", "round-robin")) {
		t.Fatalf("expected round-robin strategy in prod config:\n%s", string(prodConfig))
	}
}

func TestPublishTargetsOnlyRequestedPoolStatusOnSuccess(t *testing.T) {
	ctx, _, store, nodeSvc, poolSvc := newPublishTestService(t)

	createdNodes, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@demo.example.com:443#demo-node",
	})
	if err != nil {
		t.Fatalf("Create manual node error = %v", err)
	}

	targetPool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "target-pool",
		AuthUsername:       "target-user",
		AuthPasswordSecret: "target-pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create target pool error = %v", err)
	}
	otherPool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "other-pool",
		AuthUsername:       "other-user",
		AuthPasswordSecret: "other-pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create other pool error = %v", err)
	}

	if err := poolSvc.UpdateMembers(ctx, targetPool.ID, []MemberInput{{
		SourceType:   "manual",
		SourceNodeID: createdNodes[0].ID,
		Enabled:      true,
		Weight:       1,
	}}); err != nil {
		t.Fatalf("UpdateMembers() error = %v", err)
	}

	now := time.Now().UTC()
	if _, err := store.DB.ExecContext(ctx, `UPDATE proxy_pools
		SET last_publish_status = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		"seeded", "keep-me", now, otherPool.ID,
	); err != nil {
		t.Fatalf("seed other pool status error = %v", err)
	}

	if err := poolSvc.Publish(ctx, targetPool.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	updatedTarget, err := poolSvc.Get(ctx, targetPool.ID)
	if err != nil {
		t.Fatalf("Get(target) error = %v", err)
	}
	if updatedTarget.LastPublishStatus != "published" {
		t.Fatalf("target pool LastPublishStatus = %q, want %q", updatedTarget.LastPublishStatus, "published")
	}
	if updatedTarget.LastError != "" {
		t.Fatalf("target pool LastError = %q, want empty", updatedTarget.LastError)
	}
	if updatedTarget.LastPublishedAt == nil {
		t.Fatalf("target pool LastPublishedAt should be set")
	}

	updatedOther, err := poolSvc.Get(ctx, otherPool.ID)
	if err != nil {
		t.Fatalf("Get(other) error = %v", err)
	}
	if updatedOther.LastPublishStatus != "seeded" {
		t.Fatalf("other pool LastPublishStatus = %q, want %q", updatedOther.LastPublishStatus, "seeded")
	}
	if updatedOther.LastError != "keep-me" {
		t.Fatalf("other pool LastError = %q, want %q", updatedOther.LastError, "keep-me")
	}
	if updatedOther.LastPublishedAt != nil {
		t.Fatalf("other pool LastPublishedAt = %v, want nil", updatedOther.LastPublishedAt)
	}
}

func TestPublishFailureTargetsOnlyRequestedPoolStatus(t *testing.T) {
	ctx, _, store, _, poolSvc := newPublishTestService(t)

	targetPool, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "target-pool",
		AuthUsername:       "target-user",
		AuthPasswordSecret: "target-pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create target pool error = %v", err)
	}

	now := time.Now().UTC()
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO proxy_pools (
			id, name, auth_username, auth_password_secret, strategy, failover_enabled, enabled,
			last_publish_status, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(40000), "broken-other-pool", "broken-user", "broken-pass", "round_robin", 1, 1,
		"seeded", "keep-me", now, now,
	); err != nil {
		t.Fatalf("insert unrelated broken pool error = %v", err)
	}

	err = poolSvc.Publish(ctx, targetPool.ID)
	if err == nil {
		t.Fatalf("Publish() error = nil, want failure")
	}

	updatedTarget, getErr := poolSvc.Get(ctx, targetPool.ID)
	if getErr != nil {
		t.Fatalf("Get(target) error = %v", getErr)
	}
	if updatedTarget.LastPublishStatus != "failed" {
		t.Fatalf("target pool LastPublishStatus = %q, want %q", updatedTarget.LastPublishStatus, "failed")
	}
	if updatedTarget.LastError == "" {
		t.Fatalf("target pool LastError should be set")
	}

	var otherStatus string
	var otherError string
	rowErr := store.DB.QueryRowContext(ctx, `SELECT last_publish_status, last_error FROM proxy_pools WHERE id = ?`, int64(40000)).Scan(&otherStatus, &otherError)
	if rowErr != nil {
		t.Fatalf("QueryRowContext(other) error = %v", rowErr)
	}
	if otherStatus != "seeded" {
		t.Fatalf("other pool LastPublishStatus = %q, want %q", otherStatus, "seeded")
	}
	if otherError != "keep-me" {
		t.Fatalf("other pool LastError = %q, want %q", otherError, "keep-me")
	}
}

func newPublishTestService(t *testing.T) (context.Context, config.App, *db.Store, *nodes.Service, *Service) {
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

	broker := events.NewBroker()
	nodeSvc := nodes.NewService(store, broker)
	subSvc := subscriptions.NewService(store, settingsSvc, broker)
	mihomoMgr := mihomo.NewManager(mihomo.Options{
		BinaryPath:          "nonexistent-mihomo-binary",
		RuntimeDir:          cfg.RuntimeDir,
		ProdConfigPath:      cfg.ProdConfigPath,
		ProbeConfigPath:     cfg.ProbeConfigPath,
		ProdControllerAddr:  cfg.ProdControllerAddr,
		ProbeControllerAddr: cfg.ProbeControllerAddr,
		ProbeMixedPort:      cfg.ProbeMixedPort,
	})
	poolSvc := NewService(store, settingsSvc, nodeSvc, subSvc, mihomoMgr, broker)
	return ctx, cfg, store, nodeSvc, poolSvc
}
