package pools

import (
	"strings"
	"testing"

	"unified-proxy-pool/internal/models"
)

func TestBuildPublishBundle(t *testing.T) {
	poolList := []models.ProxyPool{
		{
			ID:                 1,
			Name:               "demo",
			Strategy:           "round_robin",
			Enabled:            true,
			AuthUsername:       "user",
			AuthPasswordSecret: "pass",
		},
	}
	member := models.RuntimeNode{
		SourceType:     "manual",
		SourceNodeID:   10,
		DisplayName:    "node-a",
		Protocol:       "trojan",
		Server:         "demo.example.com",
		Port:           443,
		Enabled:        true,
		NormalizedJSON: `{"type":"trojan","server":"demo.example.com","port":443,"password":"secret"}`,
	}

	bundle, err := BuildPublishBundle(
		"secret-token",
		"127.0.0.1:19090",
		"127.0.0.1:19091",
		17891,
		"https://www.gstatic.com/generate_204",
		"debug",
		poolList,
		map[int64][]models.RuntimeNode{1: {member}},
		[]models.RuntimeNode{member},
	)
	if err != nil {
		t.Fatalf("BuildPublishBundle() error = %v", err)
	}

	prod := string(bundle.ProdConfig)
	probe := string(bundle.ProbeConfig)
	if !strings.Contains(prod, "listeners:") || !strings.Contains(prod, "pool-group-1") || !strings.Contains(prod, "round-robin") || !strings.Contains(prod, "log-level: debug") {
		t.Fatalf("unexpected prod config:\n%s", prod)
	}
	if !strings.Contains(prod, "listen: 127.0.0.1") || !strings.Contains(prod, "port: 30001") || !strings.Contains(prod, "type: mixed") {
		t.Fatalf("expected internal mixed listener in prod config:\n%s", prod)
	}
	if !strings.Contains(prod, "username: user") || !strings.Contains(prod, "password: pass") {
		t.Fatalf("expected listener auth in prod config:\n%s", prod)
	}
	if !strings.Contains(probe, "mixed-port: 17891") ||
		!strings.Contains(probe, "GLOBAL") ||
		!strings.Contains(probe, "manual-10-node-a") ||
		!strings.Contains(probe, "SPEED_SLOT_1") ||
		!strings.Contains(probe, "speed-slot-1") ||
		!strings.Contains(probe, "port: 17892") ||
		!strings.Contains(probe, "log-level: debug") {
		t.Fatalf("unexpected probe config:\n%s", probe)
	}
}

func TestBuildPublishBundleRespectsFailoverToggleForLoadBalance(t *testing.T) {
	member := models.RuntimeNode{
		SourceType:     "manual",
		SourceNodeID:   10,
		DisplayName:    "node-a",
		Protocol:       "trojan",
		Server:         "demo.example.com",
		Port:           443,
		Enabled:        true,
		NormalizedJSON: `{"type":"trojan","server":"demo.example.com","port":443,"password":"secret"}`,
	}

	withFailover, err := BuildPublishBundle(
		"secret-token",
		"127.0.0.1:19090",
		"127.0.0.1:19091",
		17891,
		"https://www.gstatic.com/generate_204",
		"debug",
		[]models.ProxyPool{{
			ID:              1,
			Name:            "with-failover",
			Strategy:        "round_robin",
			FailoverEnabled: true,
			Enabled:         true,
		}},
		map[int64][]models.RuntimeNode{1: {member}},
		[]models.RuntimeNode{member},
	)
	if err != nil {
		t.Fatalf("BuildPublishBundle(withFailover) error = %v", err)
	}

	withoutFailover, err := BuildPublishBundle(
		"secret-token",
		"127.0.0.1:19090",
		"127.0.0.1:19091",
		17891,
		"https://www.gstatic.com/generate_204",
		"debug",
		[]models.ProxyPool{{
			ID:              2,
			Name:            "without-failover",
			Strategy:        "round_robin",
			FailoverEnabled: false,
			Enabled:         true,
		}},
		map[int64][]models.RuntimeNode{2: {member}},
		[]models.RuntimeNode{member},
	)
	if err != nil {
		t.Fatalf("BuildPublishBundle(withoutFailover) error = %v", err)
	}

	withConfig := string(withFailover.ProdConfig)
	withoutConfig := string(withoutFailover.ProdConfig)
	if !strings.Contains(withConfig, "url: https://www.gstatic.com/generate_204") || !strings.Contains(withConfig, "interval: 300") || !strings.Contains(withConfig, "lazy: true") {
		t.Fatalf("expected health-check fields when failover is enabled:\n%s", withConfig)
	}
	if strings.Contains(withoutConfig, "url: https://www.gstatic.com/generate_204") || strings.Contains(withoutConfig, "interval: 300") || strings.Contains(withoutConfig, "lazy: true") {
		t.Fatalf("did not expect health-check fields when failover is disabled:\n%s", withoutConfig)
	}
}

func TestBuildProbeInventoryConfigSanitizesLegacyTransportTypeOverride(t *testing.T) {
	member := models.RuntimeNode{
		SourceType:     "subscription",
		SourceNodeID:   55,
		DisplayName:    "legacy-vless",
		Protocol:       "vless",
		Server:         "demo.example.com",
		Port:           443,
		Enabled:        true,
		NormalizedJSON: `{"type":"tcp","server":"demo.example.com","port":443,"uuid":"uuid-1","security":"reality"}`,
	}

	payload, err := BuildProbeInventoryConfig(
		"secret-token",
		"127.0.0.1:19091",
		17891,
		"info",
		[]models.RuntimeNode{member},
	)
	if err != nil {
		t.Fatalf("BuildProbeInventoryConfig() error = %v", err)
	}

	config := string(payload)
	if !strings.Contains(config, "type: vless") {
		t.Fatalf("expected sanitized proxy type in probe config:\n%s", config)
	}
	if !strings.Contains(config, "network: tcp") {
		t.Fatalf("expected transport to be preserved as network in probe config:\n%s", config)
	}
	if strings.Contains(config, "type: tcp") {
		t.Fatalf("did not expect legacy transport type override in probe config:\n%s", config)
	}
}

// Repro (pre-fix): leave one ss with cipher "�G" at dash.zendegizibast.ir:2087,
// publish, then mihomo logs:
//   Parse config error: proxy 3021: ... cipher: �G initialize error: unknown method: �G
//   mihomo probe exited unexpectedly
// :7893 times out while :7891 /api/health stays 200.
func TestBuildProbeInventoryConfigDropsBadSSAndFixesALPN(t *testing.T) {
	bad := models.RuntimeNode{
		SourceType:     "subscription",
		SourceNodeID:   3021,
		DisplayName:    "zendegi",
		Protocol:       "ss",
		Server:         "dash.zendegizibast.ir",
		Port:           2087,
		Enabled:        true,
		NormalizedJSON: `{"type":"ss","server":"dash.zendegizibast.ir","port":2087,"cipher":"\u0083G","password":"x"}`,
	}
	good := models.RuntimeNode{
		SourceType:     "subscription",
		SourceNodeID:   1,
		DisplayName:    "ok-ss",
		Protocol:       "ss",
		Server:         "1.2.3.4",
		Port:           8388,
		Enabled:        true,
		NormalizedJSON: `{"type":"ss","server":"1.2.3.4","port":8388,"cipher":"aes-256-gcm","password":"secret"}`,
	}
	alpnStr := models.RuntimeNode{
		SourceType:     "subscription",
		SourceNodeID:   2,
		DisplayName:    "alpn-str",
		Protocol:       "trojan",
		Server:         "5.6.7.8",
		Port:           443,
		Enabled:        true,
		NormalizedJSON: `{"type":"trojan","server":"5.6.7.8","port":443,"password":"p","alpn":"h2"}`,
	}
	payload, err := BuildProbeInventoryConfig("secret", "127.0.0.1:19091", 17891, "info", []models.RuntimeNode{bad, good, alpnStr})
	if err != nil {
		t.Fatalf("BuildProbeInventoryConfig() error = %v", err)
	}
	config := string(payload)
	if strings.Contains(config, "dash.zendegizibast.ir") || strings.Contains(config, "cipher: \x83G") {
		t.Fatalf("bad ss node leaked into probe config (would fatal mihomo):\n%s", config)
	}
	if !strings.Contains(config, "1.2.3.4") || !strings.Contains(config, "aes-256-gcm") {
		t.Fatalf("good ss node missing from probe config:\n%s", config)
	}
	if !strings.Contains(config, "5.6.7.8") {
		t.Fatalf("trojan with string alpn was dropped:\n%s", config)
	}
	if strings.Contains(config, "alpn: h2\n") || strings.Contains(config, "alpn: h2\r") {
		t.Fatalf("alpn written as scalar (mihomo: 'alpn' is not a slice):\n%s", config)
	}
}

func TestBuildPublishBundleNormalizesUnsupportedLogLevelToInfo(t *testing.T) {
	member := models.RuntimeNode{
		SourceType:     "manual",
		SourceNodeID:   10,
		DisplayName:    "node-a",
		Protocol:       "trojan",
		Server:         "demo.example.com",
		Port:           443,
		Enabled:        true,
		NormalizedJSON: `{"type":"trojan","server":"demo.example.com","port":443,"password":"secret"}`,
	}

	bundle, err := BuildPublishBundle(
		"secret-token",
		"127.0.0.1:19090",
		"127.0.0.1:19091",
		17891,
		"https://www.gstatic.com/generate_204",
		"verbose",
		[]models.ProxyPool{{
			ID:       1,
			Name:     "demo",
			Strategy: "round_robin",
			Enabled:  true,
		}},
		map[int64][]models.RuntimeNode{1: {member}},
		[]models.RuntimeNode{member},
	)
	if err != nil {
		t.Fatalf("BuildPublishBundle() error = %v", err)
	}

	if !strings.Contains(string(bundle.ProdConfig), "log-level: info") {
		t.Fatalf("expected prod config to fall back to info:\n%s", string(bundle.ProdConfig))
	}
	if !strings.Contains(string(bundle.ProbeConfig), "log-level: info") {
		t.Fatalf("expected probe config to fall back to info:\n%s", string(bundle.ProbeConfig))
	}
}
