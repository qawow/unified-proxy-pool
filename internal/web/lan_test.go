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
