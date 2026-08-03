package subscriptions

import (
	"encoding/base64"
	"testing"
)

func TestParseSubscriptionContentFromBase64List(t *testing.T) {
	rawList := "trojan://password@example.com:443#node-a\nvless://uuid@example.org:8443#node-b"
	encoded := base64.StdEncoding.EncodeToString([]byte(rawList))
	result := ParseSubscriptionContent(encoded)
	if len(result.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d with errs %v", len(result.Nodes), result.Errors)
	}
	if result.Nodes[0].Protocol != "trojan" || result.Nodes[1].Protocol != "vless" {
		t.Fatalf("unexpected protocols: %+v", result.Nodes)
	}
}

func TestParseSubscriptionContentFromBOMBase64List(t *testing.T) {
	rawList := "vless://uuid@example.org:8443#node-b"
	encoded := "\uFEFF" + base64.StdEncoding.EncodeToString([]byte(rawList))
	result := ParseSubscriptionContent(encoded)
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d with errs %v", len(result.Nodes), result.Errors)
	}
	if result.Nodes[0].Protocol != "vless" {
		t.Fatalf("unexpected protocol: %+v", result.Nodes[0])
	}
}
