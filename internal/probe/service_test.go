package probe

import (
	"context"
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
	"unified-proxy-pool/internal/nodes"
	"unified-proxy-pool/internal/settings"
	"unified-proxy-pool/internal/subscriptions"
)

func TestSyncProbeInventorySkipsReloadWhenInventoryUnchanged(t *testing.T) {
	ctx, cfg, settingsSvc, nodeSvc, svc := newProbeTestService(t)

	createdNodes, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@demo.example.com:443#demo-node",
	})
	if err != nil {
		t.Fatalf("Create manual node error = %v", err)
	}

	currentSettings, err := settingsSvc.Get(ctx)
	if err != nil {
		t.Fatalf("Get() settings error = %v", err)
	}

	if _, err := svc.syncProbeInventoryAndGetNode(ctx, "manual", createdNodes[0].ID, currentSettings.MihomoControllerSecret, currentSettings.LogLevel, -1); err != nil {
		t.Fatalf("first syncProbeInventoryAndGetNode() error = %v", err)
	}
	firstInfo, err := os.Stat(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("Stat(first) error = %v", err)
	}
	firstConfig, err := os.ReadFile(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(first) error = %v", err)
	}

	time.Sleep(25 * time.Millisecond)

	if _, err := svc.syncProbeInventoryAndGetNode(ctx, "manual", createdNodes[0].ID, currentSettings.MihomoControllerSecret, currentSettings.LogLevel, -1); err != nil {
		t.Fatalf("second syncProbeInventoryAndGetNode() error = %v", err)
	}
	secondInfo, err := os.Stat(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("Stat(second) error = %v", err)
	}
	secondConfig, err := os.ReadFile(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(second) error = %v", err)
	}

	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) {
		t.Fatalf("expected unchanged inventory to keep probe config mtime, got %v then %v", firstInfo.ModTime(), secondInfo.ModTime())
	}
	if string(secondConfig) != string(firstConfig) {
		t.Fatalf("expected unchanged inventory to keep probe config content identical")
	}
}

func TestSyncProbeInventoryReloadsWhenInventoryChanges(t *testing.T) {
	ctx, cfg, settingsSvc, nodeSvc, svc := newProbeTestService(t)

	createdNodes, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@demo.example.com:443#demo-node",
	})
	if err != nil {
		t.Fatalf("Create manual node error = %v", err)
	}

	currentSettings, err := settingsSvc.Get(ctx)
	if err != nil {
		t.Fatalf("Get() settings error = %v", err)
	}

	if _, err := svc.syncProbeInventoryAndGetNode(ctx, "manual", createdNodes[0].ID, currentSettings.MihomoControllerSecret, currentSettings.LogLevel, -1); err != nil {
		t.Fatalf("first syncProbeInventoryAndGetNode() error = %v", err)
	}
	firstInfo, err := os.Stat(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("Stat(first) error = %v", err)
	}

	time.Sleep(25 * time.Millisecond)

	if _, _, err := nodeSvc.Create(ctx, nodes.CreateRequest{
		Content: "trojan://password@backup.example.com:443#backup-node",
	}); err != nil {
		t.Fatalf("Create second manual node error = %v", err)
	}

	if _, err := svc.syncProbeInventoryAndGetNode(ctx, "manual", createdNodes[0].ID, currentSettings.MihomoControllerSecret, currentSettings.LogLevel, -1); err != nil {
		t.Fatalf("second syncProbeInventoryAndGetNode() error = %v", err)
	}
	secondInfo, err := os.Stat(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("Stat(second) error = %v", err)
	}
	secondConfig, err := os.ReadFile(cfg.ProbeConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(second) error = %v", err)
	}

	if !secondInfo.ModTime().After(firstInfo.ModTime()) {
		t.Fatalf("expected changed inventory to refresh probe config mtime, got %v then %v", firstInfo.ModTime(), secondInfo.ModTime())
	}
	if !strings.Contains(string(secondConfig), "backup-node") {
		t.Fatalf("expected refreshed probe config to include new node:\n%s", string(secondConfig))
	}
}

func newProbeTestService(t *testing.T) (context.Context, config.App, *settings.Service, *nodes.Service, *Service) {
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
	svc := NewService(settingsSvc, store, nodeSvc, subSvc, nil, mihomoMgr, broker)
	return ctx, cfg, settingsSvc, nodeSvc, svc
}
