package nodes

import "testing"

// Production mihomo fatals we have already seen. SanitizeProxyMap must either
// rewrite them into a legal value or reject the node. A silent pass is a
// regression: the next publish will kill probe again.
func TestSanitizeProxyMapProductionFatals(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]any
		wantErr bool
		check   func(t *testing.T, out map[string]any)
	}{
		{
			name: "ss garbage cipher dash.zendegizibast.ir",
			in: map[string]any{
				"type": "ss", "server": "dash.zendegizibast.ir", "port": 2087,
				"cipher": "\x83G", "password": "x",
			},
			wantErr: true,
		},
		{
			name: "ss unknown method",
			in: map[string]any{
				"type": "ss", "server": "1.1.1.1", "port": 443,
				"cipher": "not-a-cipher", "password": "x",
			},
			wantErr: true,
		},
		{
			name: "alpn string becomes list",
			in: map[string]any{
				"type": "trojan", "server": "a.example", "port": 443,
				"password": "p", "alpn": "h2,http/1.1",
			},
			check: func(t *testing.T, out map[string]any) {
				list, ok := out["alpn"].([]string)
				if !ok || len(list) != 2 {
					t.Fatalf("alpn = %#v", out["alpn"])
				}
			},
		},
		{
			name: "vless encryption none= node 104004",
			in: map[string]any{
				"type": "vless", "server": "85.133.215.108", "port": 235,
				"uuid": "u", "encryption": "none=",
			},
			check: func(t *testing.T, out map[string]any) {
				if out["encryption"] != "none" {
					t.Fatalf("encryption = %#v, want none", out["encryption"])
				}
			},
		},
		{
			name: "vless encryption none==",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "encryption": "none==",
			},
			check: func(t *testing.T, out map[string]any) {
				if out["encryption"] != "none" {
					t.Fatalf("encryption = %#v", out["encryption"])
				}
			},
		},
		{
			name: "vless unknown encryption dropped",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "encryption": "garbage",
			},
			wantErr: true,
		},
		{
			name: "vless flow trailing equals stripped",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "encryption": "none", "flow": "xtls-rprx-vision=",
			},
			check: func(t *testing.T, out map[string]any) {
				if out["flow"] != "xtls-rprx-vision" {
					t.Fatalf("flow = %#v", out["flow"])
				}
			},
		},
		{
			name: "good ss kept",
			in: map[string]any{
				"type": "ss", "server": "1.2.3.4", "port": 8388,
				"cipher": "aes-256-gcm", "password": "secret",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SanitizeProxyMap(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil (would fatal mihomo on publish)")
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitizeProxyMap() error = %v", err)
			}
			if tc.check != nil {
				tc.check(t, tc.in)
			}
		})
	}
}
