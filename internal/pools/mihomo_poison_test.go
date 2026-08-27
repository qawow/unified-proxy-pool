package pools

import (
	"strings"
	"testing"

	"unified-proxy-pool/internal/models"
)

// These payloads have already killed a running mihomo probe in production.
// BuildProbeInventoryConfig must never emit the raw poison into YAML, and a
// good sibling node in the same inventory must still be published.
func TestProbeYAMLNeverContainsMihomoFatals(t *testing.T) {
	good := models.RuntimeNode{
		SourceType:     "subscription",
		SourceNodeID:   1,
		DisplayName:    "keeper",
		Protocol:       "ss",
		Server:         "203.0.113.10",
		Port:           8388,
		Enabled:        true,
		NormalizedJSON: `{"type":"ss","server":"203.0.113.10","port":8388,"cipher":"aes-256-gcm","password":"secret"}`,
	}
	cases := []struct {
		name   string
		poison models.RuntimeNode
		forbid []string
	}{
		{
			name: "ss unknown method",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 3021, DisplayName: "zendegi",
				Protocol: "ss", Server: "dash.zendegizibast.ir", Port: 2087, Enabled: true,
				NormalizedJSON: `{"type":"ss","server":"dash.zendegizibast.ir","port":2087,"cipher":"\u0083G","password":"x"}`,
			},
			forbid: []string{"dash.zendegizibast.ir", "unknown method", "cipher: \x83G"},
		},
		{
			name: "vless encryption none=",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 104004, DisplayName: "Iran",
				Protocol: "vless", Server: "85.133.215.108", Port: 235, Enabled: true,
				NormalizedJSON: `{"type":"vless","server":"85.133.215.108","port":235,"uuid":"u","encryption":"none="}`,
			},
			forbid: []string{"none=", "invaild vless encryption"},
		},
		{
			name: "alpn scalar",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 9, DisplayName: "alpn",
				Protocol: "trojan", Server: "198.51.100.9", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"trojan","server":"198.51.100.9","port":443,"password":"p","alpn":"h2"}`,
			},
			forbid: []string{"alpn: h2\n", "alpn: h2\r", "'alpn' is not a slice"},
		},
		{
			name: "tls empty string and vmess missing uuid",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 1045, DisplayName: "proxy-1045",
				Protocol: "vmess", Server: "203.0.113.45", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"vmess","server":"203.0.113.45","port":443,"tls":""}`,
			},
			forbid: []string{"203.0.113.45", "tls: \"\"", "expected type 'bool'"},
		},
		{
			name: "tls string true coerced not quoted",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 1046, DisplayName: "tls-str",
				Protocol: "vless", Server: "203.0.113.46", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"vless","server":"203.0.113.46","port":443,"uuid":"u","tls":"true"}`,
			},
			forbid: []string{`tls: "true"`, `tls: 'true'`},
		},
		{
			name: "vless missing uuid",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 2001, DisplayName: "no-uuid",
				Protocol: "vless", Server: "203.0.113.51", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"vless","server":"203.0.113.51","port":443,"tls":true}`,
			},
			forbid: []string{"203.0.113.51"},
		},
		{
			name: "trojan missing password",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 2002, DisplayName: "no-pass",
				Protocol: "trojan", Server: "203.0.113.52", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"trojan","server":"203.0.113.52","port":443}`,
			},
			forbid: []string{"203.0.113.52"},
		},
		{
			name: "ws-opts string",
			poison: models.RuntimeNode{
				SourceType: "subscription", SourceNodeID: 2003, DisplayName: "bad-ws",
				Protocol: "vless", Server: "203.0.113.53", Port: 443, Enabled: true,
				NormalizedJSON: `{"type":"vless","server":"203.0.113.53","port":443,"uuid":"u","ws-opts":"nope"}`,
			},
			forbid: []string{"ws-opts: nope", "ws-opts: \"nope\""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := BuildProbeInventoryConfig("secret", "127.0.0.1:19091", 17891, "info",
				[]models.RuntimeNode{tc.poison, good})
			if err != nil {
				t.Fatalf("BuildProbeInventoryConfig() error = %v", err)
			}
			config := string(payload)
			if !strings.Contains(config, "203.0.113.10") {
				t.Fatalf("good node missing after poison sibling:\n%s", config)
			}
			for _, s := range tc.forbid {
				if s != "" && strings.Contains(config, s) {
					t.Fatalf("poison %q leaked into probe YAML (mihomo would fatal):\n%s", s, config)
				}
			}
		})
	}
}

func TestProbeYAMLDropsMainlandChinaNodes(t *testing.T) {
	cn := models.RuntimeNode{
		SourceType: "subscription", SourceNodeID: 9, DisplayName: "中国 上海",
		Protocol: "ss", Server: "203.0.113.88", Port: 8388, Enabled: true,
		NormalizedJSON: `{"type":"ss","server":"203.0.113.88","port":8388,"cipher":"aes-256-gcm","password":"secret"}`,
	}
	hk := models.RuntimeNode{
		SourceType: "subscription", SourceNodeID: 10, DisplayName: "香港 01",
		Protocol: "ss", Server: "203.0.113.89", Port: 8388, Enabled: true,
		NormalizedJSON: `{"type":"ss","server":"203.0.113.89","port":8388,"cipher":"aes-256-gcm","password":"secret"}`,
	}
	payload, err := BuildProbeInventoryConfig("secret", "127.0.0.1:19091", 17891, "info", []models.RuntimeNode{cn, hk})
	if err != nil {
		t.Fatal(err)
	}
	config := string(payload)
	if strings.Contains(config, "203.0.113.88") || strings.Contains(config, "中国") {
		t.Fatalf("CN node leaked into probe YAML:\n%s", config)
	}
	if !strings.Contains(config, "203.0.113.89") {
		t.Fatalf("HK node missing:\n%s", config)
	}
}
