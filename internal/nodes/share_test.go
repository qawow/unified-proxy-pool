package nodes

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// A vmess ws+tls share link must produce mihomo's keys. Previously `net`,
// `tls`, `host` and `path` were left as-is, so the node was published as
// plaintext TCP.
func TestParseVMessTranslatesWSAndTLS(t *testing.T) {
	payload := map[string]any{
		"v": "2", "ps": "node-1", "add": "example.com", "port": "443",
		"id": "b831381d-6324-4d53-ad4f-8cda48b30811", "aid": "0", "scy": "auto",
		"net": "ws", "type": "none", "host": "cdn.example.com", "path": "/ray",
		"tls": "tls", "sni": "cdn.example.com",
	}
	raw, _ := json.Marshal(payload)
	node, err := parseVMess("vmess://" + base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("parseVMess: %v", err)
	}
	n := node.Normalized
	if n["network"] != "ws" {
		t.Errorf("network = %v, want ws", n["network"])
	}
	if n["tls"] != true {
		t.Errorf("tls = %v, want true", n["tls"])
	}
	if n["servername"] != "cdn.example.com" {
		t.Errorf("servername = %v, want cdn.example.com", n["servername"])
	}
	ws, ok := n["ws-opts"].(map[string]any)
	if !ok {
		t.Fatalf("ws-opts missing: %+v", n)
	}
	if ws["path"] != "/ray" {
		t.Errorf("ws path = %v, want /ray", ws["path"])
	}
	headers, _ := ws["headers"].(map[string]any)
	if headers == nil || headers["Host"] != "cdn.example.com" {
		t.Errorf("ws Host header = %v", headers)
	}
	if n["uuid"] != "b831381d-6324-4d53-ad4f-8cda48b30811" {
		t.Errorf("uuid = %v", n["uuid"])
	}
	if _, leftover := n["net"]; leftover {
		t.Error("raw share-link key 'net' survived translation")
	}
}

// vless + reality: security/pbk/sid/fp are share-link names mihomo does not read.
func TestParseVLESSTranslatesReality(t *testing.T) {
	uri := "vless://b831381d-6324-4d53-ad4f-8cda48b30811@example.com:443" +
		"?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome" +
		"&pbk=xhbPHkPPFGmz8gVsN4Y2Cn3QIhFrLBGmxYY8YZTLzUE&sid=abcd1234&type=grpc&serviceName=grpcsvc#reality-node"
	node, err := parseSimpleURLNode("vless", uri)
	if err != nil {
		t.Fatalf("parseSimpleURLNode: %v", err)
	}
	n := node.Normalized
	if n["tls"] != true {
		t.Errorf("tls = %v, want true", n["tls"])
	}
	if n["servername"] != "www.microsoft.com" {
		t.Errorf("servername = %v", n["servername"])
	}
	if n["client-fingerprint"] != "chrome" {
		t.Errorf("client-fingerprint = %v", n["client-fingerprint"])
	}
	ro, ok := n["reality-opts"].(map[string]any)
	if !ok {
		t.Fatalf("reality-opts missing: %+v", n)
	}
	if ro["public-key"] != "xhbPHkPPFGmz8gVsN4Y2Cn3QIhFrLBGmxYY8YZTLzUE" {
		t.Errorf("public-key = %v", ro["public-key"])
	}
	if ro["short-id"] != "abcd1234" {
		t.Errorf("short-id = %v", ro["short-id"])
	}
	grpc, ok := n["grpc-opts"].(map[string]any)
	if !ok || grpc["grpc-service-name"] != "grpcsvc" {
		t.Errorf("grpc-opts = %v", n["grpc-opts"])
	}
	for _, k := range []string{"security", "pbk", "sid", "fp", "sni"} {
		if _, leftover := n[k]; leftover {
			t.Errorf("raw share-link key %q survived translation", k)
		}
	}
}

// SIP002 with a path component and an obfs plugin. The path used to make
// SplitHostPort/Atoi fail, dropping the node entirely.
func TestParseSSSIP002WithPluginAndPath(t *testing.T) {
	userInfo := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secretpass"))
	uri := "ss://" + userInfo + "@example.com:8388/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#ss-node"
	node, err := parseSS(uri)
	if err != nil {
		t.Fatalf("parseSS: %v", err)
	}
	if node.Port != 8388 {
		t.Fatalf("port = %d, want 8388", node.Port)
	}
	n := node.Normalized
	if n["cipher"] != "aes-128-gcm" || n["password"] != "secretpass" {
		t.Fatalf("auth = %v / %v", n["cipher"], n["password"])
	}
	if n["plugin"] != "obfs" {
		t.Fatalf("plugin = %v, want obfs", n["plugin"])
	}
	opts, ok := n["plugin-opts"].(map[string]any)
	if !ok {
		t.Fatalf("plugin-opts missing: %+v", n)
	}
	if opts["mode"] != "http" {
		t.Errorf("plugin mode = %v, want http", opts["mode"])
	}
	if opts["host"] != "bing.com" {
		t.Errorf("plugin host = %v, want bing.com", opts["host"])
	}
}

// An obfs plugin without a mode makes mihomo abort the whole config
// ("obfs mode error"), so the plugin must be dropped rather than passed on.
func TestApplySSPluginDropsIncompleteObfs(t *testing.T) {
	p := map[string]any{"type": "ss", "plugin": "obfs-local"}
	applySSPlugin(p)
	if _, ok := p["plugin"]; ok {
		t.Fatalf("incomplete obfs plugin kept: %+v", p)
	}
}
