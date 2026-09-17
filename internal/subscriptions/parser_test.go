package subscriptions

import (
	"encoding/base64"
	"strings"
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

func TestParseSubscriptionContentURLSafeBase64(t *testing.T) {
	rawList := "vless://uuid@example.org:8443#node-b"
	encoded := base64.URLEncoding.EncodeToString([]byte(rawList))
	result := ParseSubscriptionContent(encoded)
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d errs %v", len(result.Nodes), result.Errors)
	}
}

func TestParseSubscriptionContentRejectsHTML(t *testing.T) {
	result := ParseSubscriptionContent("<!DOCTYPE html><html><body>blocked</body></html>")
	if len(result.Nodes) != 0 {
		t.Fatalf("html should not parse as nodes: %+v", result.Nodes)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected html error")
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

// TestParseSubscriptionOversizedLine: a line longer than the scanner's 1MB
// buffer makes Scan stop silently. The whole point of reporting it is that the
// operator otherwise sees "no nodes parsed" with no explanation and assumes the
// subscription is empty, when it is one oversized line the panel never read.
func TestParseSubscriptionOversizedLine(t *testing.T) {
	// A valid node, then a 2MB line, then another valid node. Without the
	// scanner.Err() check the error is swallowed and the result looks empty.
	big := strings.Repeat("a", 2<<20)
	content := "vless://uuid@example.org:8443#first\r\n" + big + "\r\nvless://uuid@example.org:8443#last\r\n"

	result := ParseSubscriptionContent(content)
	if len(result.Errors) == 0 {
		t.Fatalf("oversized line produced no error; nodes=%d", len(result.Nodes))
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(strings.ToLower(e.Error()), "scan") || strings.Contains(strings.ToLower(e.Error()), "too long") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no scan-related error among %v", result.Errors)
	}
}
