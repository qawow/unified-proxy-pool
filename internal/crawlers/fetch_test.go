package crawlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchAllStopsAtFirstYield(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/a" {
			fmt.Fprintln(w, "1.2.3.4:8080")
			return
		}
		t.Errorf("second mirror should not be fetched, got %s", r.URL.Path)
	}))
	defer ts.Close()

	c := PlainText("t", []string{ts.URL + "/a", ts.URL + "/b"}, "http", false, true)
	items, err := FetchAll(context.Background(), NewHTTPClient(5*time.Second), c)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(items) != 1 || items[0].Host != "1.2.3.4" {
		t.Fatalf("items = %+v", items)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestFetchAllJoinsMirrorErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	c := PlainText("t", []string{ts.URL + "/a", ts.URL + "/b"}, "socks4", false, true)
	_, err := FetchAll(context.Background(), NewHTTPClient(5*time.Second), c)
	if err == nil {
		t.Fatal("want joined error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "http 502") {
		t.Fatalf("want status in error, got %s", msg)
	}
	if !strings.Contains(msg, " | ") {
		t.Fatalf("want both mirrors joined, got %s", msg)
	}
}

func TestFetchAllSkipsJsdelivrWhenBlocked(t *testing.T) {
	jsdelivrBlocked.Store(true)
	t.Cleanup(func() { jsdelivrBlocked.Store(false) })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "8.8.8.8:1080")
	}))
	defer ts.Close()

	c := PlainText("t", []string{
		"https://cdn.jsdelivr.net/gh/TheSpeedX/SOCKS-List@master/socks4.txt",
		ts.URL,
	}, "socks4", false, true)
	items, err := FetchAll(context.Background(), NewHTTPClient(5*time.Second), c)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(items) != 1 || items[0].Host != "8.8.8.8" {
		t.Fatalf("items = %+v", items)
	}
}

func TestJsdelivrTimeoutMarksBlocked(t *testing.T) {
	if !isJsdelivrURL("https://cdn.jsdelivr.net/gh/x/y@z/a.txt") {
		t.Fatal("jsdelivr URL not detected")
	}
	if isJsdelivrURL("https://ghproxy.net/https://raw.githubusercontent.com/x") {
		t.Fatal("ghproxy must not be treated as jsdelivr")
	}
	if !isTimeoutish(fmt.Errorf(`Get "https://cdn.jsdelivr.net/gh/x": dial tcp 104.17.207.5:443: i/o timeout`)) {
		t.Fatal("dial timeout should count")
	}
}

func TestFetchAllWithFallbackRetriesNetworkError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "9.9.9.9:1080")
	}))
	defer ts.Close()

	primary := NewHTTPClientWithProxy(2*time.Second, "socks5://127.0.0.1:1")
	fallback := NewHTTPClient(5 * time.Second)
	c := PlainText("t", []string{ts.URL}, "socks5", false, true)
	items, err := FetchAllWithFallback(context.Background(), primary, fallback, c)
	if err != nil {
		t.Fatalf("FetchAllWithFallback: %v", err)
	}
	if len(items) != 1 || items[0].Host != "9.9.9.9" {
		t.Fatalf("items = %+v", items)
	}
}

func TestShouldRetryViaProxy(t *testing.T) {
	if shouldRetryViaProxy(fmt.Errorf("http 404")) {
		t.Fatal("404 must not retry via proxy")
	}
	if !shouldRetryViaProxy(fmt.Errorf("net/http: TLS handshake timeout")) {
		t.Fatal("TLS timeout should retry")
	}
}

func TestResolveScrapeProxy(t *testing.T) {
	if got := ResolveScrapeProxy("7892", "0.0.0.0:7892", "0.0.0.0:7893"); got != "http://127.0.0.1:7892" {
		t.Fatalf("direct shortcut = %q", got)
	}
	if got := ResolveScrapeProxy("chain", "0.0.0.0:7892", "[::]:7893"); got != "http://127.0.0.1:7893" {
		t.Fatalf("chain shortcut = %q", got)
	}
	if got := ResolveScrapeProxy("none", "0.0.0.0:7892", "0.0.0.0:7893"); got != "none" {
		t.Fatalf("none = %q", got)
	}
	if got := ResolveScrapeProxy("socks5://10.0.0.1:1080", "", ""); got != "socks5://10.0.0.1:1080" {
		t.Fatalf("passthrough = %q", got)
	}
}
