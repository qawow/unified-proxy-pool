package nodes

import (
	"encoding/base64"
	"testing"
)

func TestParseSSNode(t *testing.T) {
	node, err := ParseNodeURI("ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pass@example.com:8388")) + "#hk")
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}
	if node.Protocol != "ss" || node.Server != "example.com" || node.Port != 8388 || node.DisplayName != "hk" {
		t.Fatalf("unexpected ss node: %+v", node)
	}
}

func TestParseSSSIP002Base64UserInfo(t *testing.T) {
	// ss://base64(method:password)@host:port#name  — the form that previously
	// failed with "invalid ss auth" because userinfo has no raw colon.
	userinfo := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:Butterfly123"))
	raw := "ss://" + userinfo + "@103.236.66.4:10001#%E4%BA%9A%E5%A4%AA%E5%9C%B0%E5%8C%BA"
	node, err := ParseNodeURI(raw)
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}
	if node.Protocol != "ss" || node.Server != "103.236.66.4" || node.Port != 10001 {
		t.Fatalf("unexpected ss node: %+v", node)
	}
	if got := node.Normalized["cipher"]; got != "aes-256-gcm" {
		t.Fatalf("cipher = %#v, want aes-256-gcm", got)
	}
	if got := node.Normalized["password"]; got != "Butterfly123" {
		t.Fatalf("password = %#v, want Butterfly123", got)
	}
}

func TestParseHy2Alias(t *testing.T) {
	raw := "hy2://sOiWCQ2AdIV0OWNuqQVyWp4JZnRxdyLROSjX@faq.wwwinternetvideo.click:443/?insecure=1&sni=cabinet.example.com#%E8%8B%B1%E5%9B%BD"
	node, err := ParseNodeURI(raw)
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}
	if node.Protocol != "hysteria2" {
		t.Fatalf("Protocol = %q, want hysteria2", node.Protocol)
	}
	if node.Server != "faq.wwwinternetvideo.click" || node.Port != 443 {
		t.Fatalf("unexpected hy2 endpoint: %+v", node)
	}
	if got := node.Normalized["type"]; got != "hysteria2" {
		t.Fatalf("normalized type = %#v, want hysteria2", got)
	}
	if got := node.Normalized["password"]; got != "sOiWCQ2AdIV0OWNuqQVyWp4JZnRxdyLROSjX" {
		t.Fatalf("password = %#v", got)
	}
	if got := node.Normalized["skip-cert-verify"]; got != true {
		t.Fatalf("skip-cert-verify = %#v, want true", got)
	}
}

func TestParseVMessNode(t *testing.T) {
	payload := `{"v":"2","ps":"vmess-node","add":"vmess.example.com","port":"443","id":"uuid"}`
	node, err := ParseNodeURI("vmess://" + base64.StdEncoding.EncodeToString([]byte(payload)))
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}
	if node.Protocol != "vmess" || node.Server != "vmess.example.com" || node.Port != 443 {
		t.Fatalf("unexpected vmess node: %+v", node)
	}
}

func TestParseYAMLNodes(t *testing.T) {
	raw := `
proxies:
  - name: direct-a
    type: trojan
    server: demo.example.com
    port: 443
    password: secret
`
	nodes, errs := ParseRawNodes(raw)
	if len(errs) != 0 {
		t.Fatalf("ParseRawNodes() errs = %v", errs)
	}
	if len(nodes) != 1 || nodes[0].Protocol != "trojan" {
		t.Fatalf("unexpected yaml parse result: %+v", nodes)
	}
}

func TestParseSimpleURLNodePreservesProtocolType(t *testing.T) {
	node, err := ParseNodeURI("vless://uuid@example.org:8443?type=tcp&security=reality#node-b")
	if err != nil {
		t.Fatalf("ParseNodeURI() error = %v", err)
	}
	if node.Protocol != "vless" {
		t.Fatalf("Protocol = %q, want %q", node.Protocol, "vless")
	}
	if got := node.Normalized["type"]; got != "vless" {
		t.Fatalf("normalized type = %#v, want %q", got, "vless")
	}
	if got := node.Normalized["network"]; got != "tcp" {
		t.Fatalf("normalized network = %#v, want %q", got, "tcp")
	}
}

func TestParseVMessNodeRejectsMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing server",
			payload: `{"v":"2","ps":"vmess-node","port":"443","id":"uuid"}`,
		},
		{
			name:    "invalid port",
			payload: `{"v":"2","ps":"vmess-node","add":"vmess.example.com","port":"0","id":"uuid"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseNodeURI("vmess://" + base64.StdEncoding.EncodeToString([]byte(tc.payload)))
			if err == nil {
				t.Fatalf("ParseNodeURI() error = nil, want malformed vmess error")
			}
		})
	}
}

func TestParseSimpleURLNodeRejectsMalformedURL(t *testing.T) {
	cases := []string{
		"vless://uuid@example.org#missing-port",
		"vless://@example.org:443#missing-credential",
		"trojan://password@example.org:70000#invalid-port",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseNodeURI(raw)
			if err == nil {
				t.Fatalf("ParseNodeURI(%q) error = nil, want validation error", raw)
			}
		})
	}
}
