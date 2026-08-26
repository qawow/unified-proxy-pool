package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"unified-proxy-pool/internal/chanpolicy"
)

// Existing scripts call /api/public/get with no parameters and parse a bare
// host:port. That must not change.
func TestPublicGetWithoutParamsStaysPlainSingleAddr(t *testing.T) {
	app, _, _ := newChannelApp(t, testProxy("10.0.0.1", 8080))
	resp, body := doJSON(t, app, nil, http.MethodGet, "/api/public/get", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != "10.0.0.1:8080" {
		t.Errorf("body = %q, want the bare address", body)
	}
}

// ?count= opts into a batch; without it the payload shape is untouched.
func TestPublicGetCountReturnsOnePerLine(t *testing.T) {
	app, _, _ := newChannelApp(t,
		testProxy("10.0.0.1", 8080),
		testProxy("10.0.0.2", 8080),
		testProxy("10.0.0.3", 8080),
	)
	resp, body := doJSON(t, app, nil, http.MethodGet, "/api/public/get?count=3", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), body)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if seen[l] {
			t.Errorf("duplicate address %q in a batch", l)
		}
		seen[l] = true
	}
}

func TestPublicGetCountIsCapped(t *testing.T) {
	app, _, _ := newChannelApp(t, testProxy("10.0.0.1", 8080))
	resp, body := doJSON(t, app, nil, http.MethodGet, "/api/public/get?count=99999", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	// Only one proxy exists, so the cap is proven by not erroring and not looping.
	if len(lines) != 1 {
		t.Errorf("got %d lines from a one-proxy pool", len(lines))
	}
}

// A banned proxy must not come back for that channel through the public API.
func TestPublicGetRespectsChannelBan(t *testing.T) {
	app, _, reg := newChannelApp(t,
		testProxy("10.0.0.1", 8080),
		testProxy("10.0.0.2", 8080),
	)
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Fatal("fixture failed to ban")
	}

	for i := 0; i < 20; i++ {
		resp, body := doJSON(t, app, nil, http.MethodGet, "/api/public/get?channel=taobao.com", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
		}
		if strings.TrimSpace(body) == "10.0.0.1:8080" {
			t.Fatal("served a proxy banned for this channel")
		}
	}
	// The same proxy is still available to a different channel.
	found := false
	for i := 0; i < 60; i++ {
		resp, body := doJSON(t, app, nil, http.MethodGet, "/api/public/get?channel=amazon.com", "")
		resp.Body.Close()
		if strings.TrimSpace(body) == "10.0.0.1:8080" {
			found = true
			break
		}
	}
	if !found {
		t.Error("the banned-for-taobao proxy never appeared for amazon; the ban is not channel-scoped")
	}
}

// ?target= must resolve to the same channel the pool derives internally.
func TestPublicGetTargetDerivesSameChannelAsBan(t *testing.T) {
	app, _, reg := newChannelApp(t,
		testProxy("10.0.0.1", 8080),
		testProxy("10.0.0.2", 8080),
	)
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})

	for i := 0; i < 20; i++ {
		resp, body := doJSON(t, app, nil, http.MethodGet,
			"/api/public/get?target=https://item.taobao.com/detail", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
		}
		if strings.TrimSpace(body) == "10.0.0.1:8080" {
			t.Fatal("?target= did not map to the channel that holds the ban")
		}
	}
}

// The authenticated random endpoint keeps its single-object payload by default.
func TestProxyRandomWithoutCountReturnsSingleObject(t *testing.T) {
	app, cookie, _ := newChannelApp(t, testProxy("10.0.0.1", 8080))
	resp, body := doJSON(t, app, cookie, http.MethodGet, "/api/proxies/random", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var envelope struct {
		Data struct {
			Addr string `json:"addr"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if envelope.Data.Addr != "10.0.0.1:8080" {
		t.Errorf("data.addr = %q, want the single proxy object shape", envelope.Data.Addr)
	}
}

func TestProxyRandomWithCountReturnsList(t *testing.T) {
	app, cookie, _ := newChannelApp(t,
		testProxy("10.0.0.1", 8080),
		testProxy("10.0.0.2", 8080),
	)
	resp, body := doJSON(t, app, cookie, http.MethodGet, "/api/proxies/random?count=2", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	data := decodeData(t, body)
	if data["count"] != float64(2) {
		t.Errorf("count = %v, want 2 (body %s)", data["count"], body)
	}
	if _, ok := data["items"]; !ok {
		t.Errorf("no items key in a batch response: %s", body)
	}
}

// When a channel has banned the whole pool, the caller still gets a proxy and is
// told the bans were bypassed.
func TestProxyRandomReportsRelaxedWhenChannelBannedAll(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})

	resp, body := doJSON(t, app, cookie, http.MethodGet,
		"/api/proxies/random?count=1&channel=taobao.com", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	data := decodeData(t, body)
	if data["relaxed"] != true {
		t.Errorf("relaxed = %v, want true so the caller knows the ban was bypassed (body %s)",
			data["relaxed"], body)
	}
}

func TestPickCountParsing(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", 0},
		{"?count=0", 0},
		{"?count=-5", 0},
		{"?count=abc", 0},
		{"?count=7", 7},
		{"?n=3", 3},
		{"?limit=4", 4},
		{fmt.Sprintf("?count=%d", maxPickCount+50), maxPickCount},
	}
	for _, c := range cases {
		req := mustRequest(t, "/api/public/get"+c.query)
		if got := pickCount(req); got != c.want {
			t.Errorf("pickCount(%q) = %d, want %d", c.query, got, c.want)
		}
	}
}

// mustRequest builds a GET request for the pure parameter-parsing tests.
func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", url, err)
	}
	return req
}
