package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/freproxies"
)

// newChannelApp returns an authenticated App with the channel registry attached
// and n validated proxies in the pool.
func newChannelApp(t *testing.T, proxies ...freproxies.Proxy) (*App, *http.Cookie, *chanpolicy.Registry) {
	t.Helper()
	app, cookie := newAuthApp(t)
	reg := chanpolicy.New(chanpolicy.Options{Policy: chanpolicy.Defaults()})
	app.channels = reg
	app.free.SetChannelPolicy(reg)

	if len(proxies) > 0 {
		ctx := context.Background()
		if _, err := app.free.Store().AddRaw(ctx, proxies); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		for _, p := range proxies {
			if err := app.free.Store().MarkValidated(ctx, p.Addr, p.LatencyMS, true); err != nil {
				t.Fatalf("MarkValidated: %v", err)
			}
		}
	}
	return app, cookie, reg
}

func testProxy(host string, port int) freproxies.Proxy {
	return freproxies.Proxy{
		Host: host, Port: port, Addr: fmt.Sprintf("%s:%d", host, port),
		Protocol: "http", Score: 50, LatencyMS: 100,
	}
}

func doJSON(t *testing.T, app *App, cookie *http.Cookie, method, path, body string) (*http.Response, string) {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "192.168.2.10:1234"
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mustRouter(t, app).ServeHTTP(rec, req)
	return rec.Result(), rec.Body.String()
}

func decodeData(t *testing.T, raw string) map[string]any {
	t.Helper()
	var envelope struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if !envelope.Success {
		t.Fatalf("response not successful: %s", envelope.Message)
	}
	return envelope.Data
}

// The report endpoint is the only route by which an HTTPS 403 can reach the pool,
// so a single report on a listed status has to produce a ban.
func TestChannelReportBansOnListedStatus(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))

	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report",
		`{"channel":"taobao.com","addr":"10.0.0.1:8080","ok":false,"status":403}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	data := decodeData(t, body)
	if data["accepted"] != float64(1) {
		t.Errorf("accepted = %v, want 1 (body %s)", data["accepted"], body)
	}
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Error("reported 403 did not ban the proxy for the channel")
	}
	if reg.Banned("amazon.com", "10.0.0.1:8080") {
		t.Error("reported ban leaked to another channel")
	}
}

// A caller that knows the URL but not the channel naming rules can send target=
// and must land on the same bucket the proxy would derive itself.
func TestChannelReportAcceptsTargetInsteadOfChannel(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))

	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report",
		`{"target":"https://item.taobao.com/x?y=1","addr":"10.0.0.1:8080","ok":false,"status":429}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Errorf("target-derived channel did not match the derived name; channels=%+v", reg.Channels())
	}
}

func TestChannelReportBatch(t *testing.T) {
	app, cookie, reg := newChannelApp(t,
		testProxy("10.0.0.1", 8080), testProxy("10.0.0.2", 8080))

	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report", `{"items":[
		{"channel":"taobao.com","addr":"10.0.0.1:8080","ok":false,"status":403},
		{"channel":"taobao.com","addr":"10.0.0.2:8080","ok":true,"status":200},
		{"channel":"","addr":"","ok":false}
	]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	data := decodeData(t, body)
	if data["accepted"] != float64(2) || data["rejected"] != float64(1) {
		t.Errorf("accepted/rejected = %v/%v, want 2/1", data["accepted"], data["rejected"])
	}
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Error("the failing proxy in the batch was not banned")
	}
	if reg.Banned("taobao.com", "10.0.0.2:8080") {
		t.Error("the succeeding proxy in the batch was banned")
	}
}

func TestChannelReportRejectsOversizedBatch(t *testing.T) {
	app, cookie, _ := newChannelApp(t)
	var items []string
	for i := 0; i < maxReportItems+1; i++ {
		items = append(items, fmt.Sprintf(`{"channel":"a.com","addr":"10.0.0.%d:80","ok":false}`, i%250))
	}
	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report",
		`{"items":[`+strings.Join(items, ",")+`]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized batch (body %s)", resp.StatusCode, body)
	}
}

func TestChannelReportRejectsInvalidJSON(t *testing.T) {
	app, cookie, _ := newChannelApp(t)
	resp, _ := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report", `{not json`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The public variant exists for LAN scripts and must work without a session.
func TestPublicChannelReportNeedsNoAuth(t *testing.T) {
	app, _, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	resp, body := doJSON(t, app, nil, http.MethodPost, "/api/public/channels/report",
		`{"channel":"taobao.com","addr":"10.0.0.1:8080","ok":false,"status":403}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Error("public report did not register")
	}
}

func TestChannelListAndBansAndUnban(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})
	if !reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Fatal("fixture failed to create a ban")
	}

	resp, body := doJSON(t, app, cookie, http.MethodGet, "/api/channels", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "taobao.com") {
		t.Fatalf("channel list status=%d body=%s", resp.StatusCode, body)
	}

	resp2, body2 := doJSON(t, app, cookie, http.MethodGet, "/api/channels/taobao.com/bans", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || !strings.Contains(body2, "10.0.0.1:8080") {
		t.Fatalf("bans status=%d body=%s", resp2.StatusCode, body2)
	}

	resp3, body3 := doJSON(t, app, cookie, http.MethodDelete,
		"/api/channels/taobao.com/bans?addr=10.0.0.1:8080", "")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("unban status=%d body=%s", resp3.StatusCode, body3)
	}
	if reg.Banned("taobao.com", "10.0.0.1:8080") {
		t.Error("proxy still banned after the unban call")
	}
}

func TestChannelResetAndDelete(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})

	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/taobao.com/reset", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", resp.StatusCode, body)
	}
	if len(reg.Bans("taobao.com")) != 0 {
		t.Error("bans survived the reset")
	}

	resp2, body2 := doJSON(t, app, cookie, http.MethodDelete, "/api/channels/taobao.com", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", resp2.StatusCode, body2)
	}
	for _, st := range reg.Channels() {
		if st.Name == "taobao.com" {
			t.Error("channel survived the delete")
		}
	}
}

// /bans must not be swallowed as a channel name by the bare {name} route.
func TestChannelRoutingDoesNotTreatBansAsChannelName(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403})

	resp, body := doJSON(t, app, cookie, http.MethodGet, "/api/channels/taobao.com/bans", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bans route status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "10.0.0.1:8080") {
		t.Errorf("bans route returned no ban data: %s", body)
	}
}

func TestChannelEndpointsDisabledWithoutRegistry(t *testing.T) {
	app, cookie := newAuthApp(t)
	app.channels = nil

	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/report",
		`{"channel":"a.com","addr":"1.2.3.4:80","ok":false}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("report status = %d, want 400 when the registry is absent (body %s)", resp.StatusCode, body)
	}

	// The list endpoint stays successful-but-empty so the panel renders instead of
	// showing an error for a disabled feature.
	resp2, body2 := doJSON(t, app, cookie, http.MethodGet, "/api/channels", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("list status = %d, want 200 (body %s)", resp2.StatusCode, body2)
	}
}

func TestPublicReportRateLimited(t *testing.T) {
	app, _, _ := newChannelApp(t, testProxy("10.0.0.1", 8080))
	publicReportLimiter.mu.Lock()
	publicReportLimiter.m = map[string]ipWindow{}
	publicReportLimiter.mu.Unlock()

	hitLimit := false
	for i := 0; i < publicReportLimit+5; i++ {
		resp, _ := doJSON(t, app, nil, http.MethodPost, "/api/public/channels/report",
			`{"channel":"rate.com","addr":"10.0.0.1:8080","ok":true}`)
		if resp.StatusCode == http.StatusTooManyRequests {
			hitLimit = true
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}
	if !hitLimit {
		t.Fatal("public report path never returned 429")
	}
}

func TestAllowlistAPIProtectsAddr(t *testing.T) {
	app, cookie, reg := newChannelApp(t, testProxy("10.0.0.1", 8080))
	resp, body := doJSON(t, app, cookie, http.MethodPost, "/api/channels/allowlist",
		`{"channel":"taobao.com","addr":"10.0.0.1:8080","reason":"bought"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allow status=%d body=%s", resp.StatusCode, body)
	}
	if b := reg.Record(chanpolicy.Outcome{Channel: "taobao.com", Addr: "10.0.0.1:8080", Status: 403}); b != nil {
		t.Fatal("allowlisted addr was still banned via Record")
	}
}

func TestChannelLogsShowTriggeringLine(t *testing.T) {
	app, cookie, _ := newChannelApp(t, testProxy("10.0.0.1", 8080))
	doJSON(t, app, cookie, http.MethodPost, "/api/channels/report",
		`{"channel":"taobao.com","addr":"10.0.0.1:8080","ok":false,"status":403}`)

	resp, body := doJSON(t, app, cookie, http.MethodGet, "/api/channels/logs?channel=taobao.com", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "10.0.0.1:8080") || !strings.Contains(body, `"banned":true`) {
		t.Errorf("logs missing the triggering line: %s", body)
	}

	resp2, body2 := doJSON(t, app, cookie, http.MethodPost, "/api/channels/logs/clear", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", resp2.StatusCode, body2)
	}
	resp3, body3 := doJSON(t, app, cookie, http.MethodGet, "/api/channels/logs", "")
	defer resp3.Body.Close()
	if strings.Contains(body3, "10.0.0.1:8080") {
		t.Errorf("log line survived Clear: %s", body3)
	}
}
