package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAPIAllowsLoopback(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/public/debug")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LAN debug status = %d, want 200", resp.StatusCode)
	}
}

func TestPublicAPIRejectsSpoofedPublicIPFromNonLoopback(t *testing.T) {
	app := newTestApp(t)
	h := mustRouter(t, app)
	req := httptest.NewRequest(http.MethodGet, "/api/public/get", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	req.Header.Set("X-Real-IP", "192.168.1.2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (X-Real-IP must not be trusted from the internet)", rec.Code)
	}
}

func TestPublicAPIAllowsPrivateRemote(t *testing.T) {
	app := newTestApp(t)
	h := mustRouter(t, app)
	req := httptest.NewRequest(http.MethodGet, "/api/public/health", nil)
	req.RemoteAddr = "192.168.2.10:9999"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for RFC1918 client", rec.Code)
	}
}

func TestPublicOpenBypassesLANGate(t *testing.T) {
	app, _ := newAuthApp(t)
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	cur, err := app.settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur.MihomoControllerSecret == "" {
		cur.MihomoControllerSecret = "test-secret"
	}
	cur.FeatureJSON = `{"public_open":true}`
	if _, _, err := app.settings.Update(ctx, cur); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	h := mustRouter(t, app)
	req := httptest.NewRequest(http.MethodGet, "/api/public/health", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public_open status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestPublicSubmitRateLimit(t *testing.T) {
	if !allowPublicSubmit("10.0.0.1") {
		t.Fatal("first submit should pass")
	}
	for i := 0; i < publicSubmitLimit; i++ {
		allowPublicSubmit("10.0.0.1")
	}
	if allowPublicSubmit("10.0.0.1") {
		t.Fatal("expected rate limit after burst")
	}
	if !allowPublicSubmit("10.0.0.2") {
		t.Fatal("other IP should not share the bucket")
	}
}

func TestPublicClientIPIgnoresXFFFromWAN(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:80"
	req.Header.Set("X-Forwarded-For", "192.168.0.8")
	if got := publicClientIP(req); got != "1.2.3.4" {
		t.Fatalf("publicClientIP = %q, want 1.2.3.4", got)
	}
}

// A loopback reverse proxy that was never declared trusted must not be able to
// launder an internet client into the LAN gate via forwarded headers.
func TestForwardedHeadersIgnoredWithoutTrustedProxy(t *testing.T) {
	app := newTestApp(t)
	h := mustRouter(t, app)
	for _, hdr := range []struct{ name, value string }{
		{"X-Real-IP", "192.168.1.2"},
		{"X-Forwarded-For", "192.168.1.2"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/public/health", nil)
		req.RemoteAddr = "8.8.8.8:1234"
		req.Header.Set(hdr.name, hdr.value)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", hdr.name, rec.Code)
		}
	}
}

// With a trusted front proxy, the rightmost untrusted XFF entry wins: nginx
// appends the real peer, so the leftmost entries are client-supplied.
func TestForwardedClientIPTakesRightmostUntrusted(t *testing.T) {
	trusted := parseCIDRs([]string{"127.0.0.1", "10.1.0.0/16"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.2, 203.0.113.7, 10.1.0.9")
	if got := forwardedClientIP(req, trusted); got != "203.0.113.7" {
		t.Fatalf("forwardedClientIP = %q, want 203.0.113.7", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Real-IP", "not-an-ip")
	if got := forwardedClientIP(req2, trusted); got != "" {
		t.Fatalf("malformed X-Real-IP accepted: %q", got)
	}
}

func TestRequireLANMessage(t *testing.T) {
	app := newTestApp(t)
	h := mustRouter(t, app)
	req := httptest.NewRequest(http.MethodGet, "/api/public/debug", nil)
	req.RemoteAddr = "203.0.113.9:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "LAN-only") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
