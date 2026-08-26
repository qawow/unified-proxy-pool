// Package netutil holds small shared networking helpers.
package netutil

import (
	"net/http"
	"strings"
)

// BrowserUserAgent is a current Chrome-on-Windows string. Sources and
// subscription hosts routinely 403 anything that says "bot" or names the
// pool; this one does neither.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// ApplyDefaultHeaders fills in the headers a real browser would send, without
// overwriting anything the caller already set. Used by crawlers, subscription
// sync, validator probes, speed tests and GeoIP — one place, so a site that
// starts rejecting us is a one-line change.
func ApplyDefaultHeaders(h http.Header) {
	if h == nil {
		return
	}
	setIfAbsent(h, "User-Agent", BrowserUserAgent)
	setIfAbsent(h, "Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	setIfAbsent(h, "Accept-Language", "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7")
	setIfAbsent(h, "Cache-Control", "no-cache")
	setIfAbsent(h, "Pragma", "no-cache")
}

func setIfAbsent(h http.Header, key, value string) {
	if h.Get(key) == "" {
		h.Set(key, value)
	}
}

// ApplySubscriptionHeaders sets User-Agent / Accept based on the subscription
// URL so Clash YAML, GitHub raw, and airport token links are not 403'd.
// Existing headers are left alone (caller overrides win).
func ApplySubscriptionHeaders(h http.Header, rawURL string) {
	if h == nil {
		return
	}
	for k, v := range HeadersForSubscriptionURL(rawURL) {
		setIfAbsent(h, k, v)
	}
}

// HeadersForSubscriptionURL returns the suggested headers for a sub URL.
func HeadersForSubscriptionURL(rawURL string) map[string]string {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	switch {
	case strings.Contains(u, "type=clash"), strings.Contains(u, "/clash"),
		strings.HasSuffix(u, ".yaml"), strings.HasSuffix(u, ".yml"):
		return map[string]string{
			"User-Agent": "clash.meta/v1.19.30",
			"Accept":     "text/yaml,text/plain,*/*",
		}
	case strings.Contains(u, "workers.dev"), strings.Contains(u, "pages.dev"),
		strings.Contains(u, "edtunnel"), strings.Contains(u, "edgetunnel"):
		return map[string]string{
			"User-Agent": "clash.meta/v1.19.30",
			"Accept":     "*/*",
		}
	case strings.Contains(u, "githubusercontent.com"), strings.Contains(u, "jsdelivr.net"),
		strings.Contains(u, "github.com"):
		return map[string]string{
			"User-Agent": "clash.meta/v1.19.30",
			"Accept":     "*/*",
		}
	case strings.Contains(u, "subscribe"), strings.Contains(u, "v2ray"), strings.Contains(u, "token="):
		return map[string]string{
			"User-Agent": "v2rayN/6.45",
			"Accept":     "*/*",
		}
	default:
		return nil
	}
}
