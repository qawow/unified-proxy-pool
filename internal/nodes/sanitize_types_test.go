package nodes

import "testing"

func TestSanitizeProxyMapCoversMihomoTypeTraps(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]any
		wantErr bool
		check   func(t *testing.T, out map[string]any)
	}{
		{
			name: "port as string",
			in: map[string]any{
				"type": "ss", "server": "1.2.3.4", "port": "8388",
				"cipher": "aes-256-gcm", "password": "x",
			},
			check: func(t *testing.T, out map[string]any) {
				if out["port"] != 8388 {
					t.Fatalf("port = %#v", out["port"])
				}
			},
		},
		{
			name: "vless missing uuid",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
			},
			wantErr: true,
		},
		{
			name: "trojan missing password",
			in: map[string]any{
				"type": "trojan", "server": "1.2.3.4", "port": 443,
			},
			wantErr: true,
		},
		{
			name: "hysteria2 missing password",
			in: map[string]any{
				"type": "hysteria2", "server": "1.2.3.4", "port": 443,
			},
			wantErr: true,
		},
		{
			name: "unknown type dropped",
			in: map[string]any{
				"type": "not-a-proxy", "server": "1.2.3.4", "port": 443,
			},
			wantErr: true,
		},
		{
			name: "ws-opts string dropped not fatal",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "ws-opts": "garbage",
			},
			check: func(t *testing.T, out map[string]any) {
				if _, ok := out["ws-opts"]; ok {
					t.Fatalf("ws-opts still present: %#v", out["ws-opts"])
				}
			},
		},
		{
			name: "smux object kept",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "smux": map[string]any{"enabled": "true"},
			},
			check: func(t *testing.T, out map[string]any) {
				m, ok := out["smux"].(map[string]any)
				if !ok {
					t.Fatalf("smux = %#v", out["smux"])
				}
				if m["enabled"] != true {
					t.Fatalf("smux.enabled = %#v", m["enabled"])
				}
			},
		},
		{
			name: "network junk stripped",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "network": "ws=",
			},
			check: func(t *testing.T, out map[string]any) {
				if out["network"] != "ws" {
					t.Fatalf("network = %#v", out["network"])
				}
			},
		},
		{
			name: "fingerprint junk deleted",
			in: map[string]any{
				"type": "vless", "server": "1.2.3.4", "port": 443,
				"uuid": "u", "client-fingerprint": "not-a-browser",
			},
			check: func(t *testing.T, out map[string]any) {
				if _, ok := out["client-fingerprint"]; ok {
					t.Fatalf("fingerprint kept: %#v", out["client-fingerprint"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SanitizeProxyMap(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, tc.in)
			}
		})
	}
}
