package pools

import (
	"context"
	"path/filepath"
	"testing"

	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/mihomo"
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
)

func TestValidateUpsertRequestRequiresAuthFields(t *testing.T) {
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

	// Missing username should fail
	if err := poolSvc.validateUpsertRequest(ctx, 0, UpsertRequest{
		Name:               "demo",
		AuthUsername:       "",
		AuthPasswordSecret: "pass",
	}); err == nil {
		t.Fatalf("expected auth validation error for missing username")
	}

	// Missing password should fail
	if err := poolSvc.validateUpsertRequest(ctx, 0, UpsertRequest{
		Name:               "demo",
		AuthUsername:       "user",
		AuthPasswordSecret: "",
	}); err == nil {
		t.Fatalf("expected auth validation error for missing password")
	}

	// Valid request should pass
	if err := poolSvc.validateUpsertRequest(ctx, 0, UpsertRequest{
		Name:               "demo",
		AuthUsername:       "user",
		AuthPasswordSecret: "pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	}); err != nil {
		t.Fatalf("validateUpsertRequest() error = %v", err)
	}

	created, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "  demo  ",
		AuthUsername:       "  user  ",
		AuthPasswordSecret: "  pass  ",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Name != "demo" || created.AuthUsername != "user" || created.AuthPasswordSecret != "pass" {
		t.Fatalf("expected normalized credentials and name, got %#v", created)
	}

	if _, err := poolSvc.Create(ctx, UpsertRequest{
		Name:               "duplicate",
		AuthUsername:       "user",
		AuthPasswordSecret: "other-pass",
		Strategy:           "round_robin",
		FailoverEnabled:    true,
		Enabled:            true,
	}); err == nil {
		t.Fatalf("expected duplicate username validation error")
	}

	lookup, err := poolSvc.LookupPoolByAuth(ctx, " user ", " pass ")
	if err != nil {
		t.Fatalf("LookupPoolByAuth() error = %v", err)
	}
	if lookup == nil || lookup.ID != created.ID {
		t.Fatalf("expected normalized auth lookup to find created pool, got %#v", lookup)
	}
}
