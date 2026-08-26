package netutil

import (
	"net/http"
	"strings"
	"testing"
)

func TestApplyDefaultHeadersFillsBrowserSet(t *testing.T) {
	h := make(http.Header)
	ApplyDefaultHeaders(h)
	if got := h.Get("User-Agent"); got != BrowserUserAgent {
		t.Errorf("User-Agent = %q", got)
	}
	if h.Get("Accept") == "" || h.Get("Accept-Language") == "" {
		t.Errorf("missing Accept headers: %v", h)
	}
	ua := h.Get("User-Agent")
	if strings.Contains(ua, "UnifiedProxyPool") || strings.Contains(ua, "compatible;") {
		t.Errorf("default UA still identifies the pool: %s", ua)
	}
}

func TestApplyDefaultHeadersDoesNotOverwriteCaller(t *testing.T) {
	h := make(http.Header)
	h.Set("User-Agent", "my-scraper/1.0")
	h.Set("Accept", "application/json")
	ApplyDefaultHeaders(h)
	if got := h.Get("User-Agent"); got != "my-scraper/1.0" {
		t.Errorf("caller User-Agent was overwritten: %s", got)
	}
	if got := h.Get("Accept"); got != "application/json" {
		t.Errorf("caller Accept was overwritten: %s", got)
	}
	if h.Get("Accept-Language") == "" {
		t.Error("Accept-Language should have been filled in")
	}
}

func TestApplyDefaultHeadersNilIsSafe(t *testing.T) {
	ApplyDefaultHeaders(nil)
}

func TestHeadersForSubscriptionURL(t *testing.T) {
	cases := []struct {
		url string
		ua  string
	}{
		{"https://nodebuf.com/dynamic?type=clash", "clash.meta/v1.19.0"},
		{"http://192.168.2.198:5001/clash", "clash.meta/v1.19.0"},
		{"https://raw.githubusercontent.com/foo/bar/main/sub", "clash.meta/v1.19.0"},
		{"https://airport.example/api/v1/client/subscribe?token=abc", "v2rayN/6.45"},
		{"https://example.com/other", ""},
	}
	for _, tc := range cases {
		got := HeadersForSubscriptionURL(tc.url)
		if tc.ua == "" {
			if got != nil {
				t.Fatalf("%s: expected no auto headers, got %v", tc.url, got)
			}
			continue
		}
		if got["User-Agent"] != tc.ua {
			t.Fatalf("%s: UA = %q, want %q", tc.url, got["User-Agent"], tc.ua)
		}
	}
}

func TestApplySubscriptionHeadersDoesNotOverride(t *testing.T) {
	h := make(http.Header)
	h.Set("User-Agent", "custom/1")
	ApplySubscriptionHeaders(h, "https://x/clash")
	if h.Get("User-Agent") != "custom/1" {
		t.Fatalf("overwrote caller UA: %q", h.Get("User-Agent"))
	}
}
