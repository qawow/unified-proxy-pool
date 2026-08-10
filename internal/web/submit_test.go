package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/apitoken"
	"unified-proxy-pool/internal/auth"
	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/settings"
)

// parseAddrLine is a pure function; test exhaustively without any I/O.
func TestParseAddrLine(t *testing.T) {
	cases := []struct {
		in        string
		wantHost  string
		wantPort  int
		wantProto string
		wantEmpty bool
	}{
		{"1.2.3.4:8080", "1.2.3.4", 8080, "http", false},
		{"http://5.6.7.8:3128", "5.6.7.8", 3128, "http", false},
		{"socks5://9.10.11.12:1080", "9.10.11.12", 1080, "socks5", false},
		{"socks4://1.2.3.4:1080", "1.2.3.4", 1080, "socks4", false},
		{"socks5://user:pass@1.2.3.4:1080", "1.2.3.4", 1080, "socks5", false},
		{"[::1]:1080", "::1", 1080, "http", false},
		{"", "", 0, "", true},
		{"#comment", "", 0, "", true},
		{"1.2.3.4:notaport", "", 0, "", true},
		{"1.2.3.4:0", "", 0, "", true},
		{"1.2.3.4:65536", "", 0, "", true},
		{"1.2.3.4", "", 0, "", true},
	}
	for _, c := range cases {
		p := parseAddrLine(c.in)
		if c.wantEmpty {
			if p.Addr != "" || p.Host != "" {
				t.Errorf("parseAddrLine(%q): expected empty proxy, got host=%q port=%d",
					c.in, p.Host, p.Port)
			}
			continue
		}
		if p.Host != c.wantHost {
			t.Errorf("parseAddrLine(%q): host = %q, want %q", c.in, p.Host, c.wantHost)
		}
		if p.Port != c.wantPort {
			t.Errorf("parseAddrLine(%q): port = %d, want %d", c.in, p.Port, c.wantPort)
		}
		if p.Protocol != c.wantProto {
			t.Errorf("parseAddrLine(%q): proto = %q, want %q", c.in, p.Protocol, c.wantProto)
		}
	}
}

// TestHandlePublicSubmit exercises the unauthenticated text/plain submit path
// end-to-end. Uses memoryStore so no Redis needed.
func TestHandlePublicSubmit(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body := "1.2.3.4:8080\n5.6.7.8:3128\n1.2.3.4:8080\n"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/public/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Success {
		t.Fatalf("success=false: %s", out.Message)
	}
	data := out.Data.(map[string]any)
	// submitted counts unique parsed lines (3 including the duplicate input);
	// added counts what AddRaw actually wrote (2, deduplicating 1.2.3.4:8080).
	submitted := int(data["submitted"].(float64))
	if submitted != 3 {
		t.Errorf("submitted = %d, want 3", submitted)
	}
	added := int(data["added"].(float64))
	if added != 2 {
		t.Errorf("added = %d, want 2 (duplicate skipped by AddRaw)", added)
	}
}

// TestHandlePublicSubmitEmptyBody checks the 400 path.
func TestHandlePublicSubmitEmptyBody(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/public/submit", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty body", resp.StatusCode)
	}
}

// TestHandlePublicSubmitGarbageLines ensures unparseable lines are silently
// skipped and do not cause a 500.
func TestHandlePublicSubmitGarbageLines(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body := "notanaddress\n# comment\n\n192.168.1.1:9090\n"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/public/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200; garbage lines must be silently skipped", resp.StatusCode)
	}
	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	data := out.Data.(map[string]any)
	if int(data["submitted"].(float64)) != 1 {
		t.Errorf("submitted = %v, want 1", data["submitted"])
	}
}

// TestHandleProxySubmitJSON exercises the JSON body path on the protected endpoint.
func TestHandleProxySubmitJSON(t *testing.T) {
	app, cookie := newAuthApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"proxies": []map[string]any{
			{"host": "10.0.0.1", "port": 3128, "protocol": "http"},
			{"host": "10.0.0.2", "port": 1080, "protocol": "socks5"},
		},
		"source": "test-script",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.Success {
		t.Fatalf("success=false: %s", out.Message)
	}
	data := out.Data.(map[string]any)
	if int(data["added"].(float64)) != 2 {
		t.Errorf("added = %v, want 2", data["added"])
	}
}

// TestHandleProxySubmitRequiresAuth ensures the protected endpoint rejects
// unauthenticated requests (no session cookie, no Bearer token).
func TestHandleProxySubmitRequiresAuth(t *testing.T) {
	app, _ := newAuthApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"proxies": []map[string]any{{"host": "1.2.3.4", "port": 8080}},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately no cookie and no Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for unauthenticated request", resp.StatusCode)
	}
}

// TestHandleProxyBatchTest submits two RFC 5737 TEST-NET addresses that will
// never connect. The test checks structure and counts, not liveness.
func TestHandleProxyBatchTest(t *testing.T) {
	app, cookie := newAuthApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"addrs":       []string{"192.0.2.1:8080", "192.0.2.2:8080"},
		"timeout_ms":  300,
		"concurrency": 2,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/batch-test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.Success {
		t.Fatalf("success=false: %s", out.Message)
	}
	data := out.Data.(map[string]any)
	results, ok := data["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results has %d items, want 2", len(results))
	}
	for i, r := range results {
		rm := r.(map[string]any)
		if _, hasAddr := rm["addr"]; !hasAddr {
			t.Errorf("result[%d] missing addr field", i)
		}
		if _, hasOK := rm["ok"]; !hasOK {
			t.Errorf("result[%d] missing ok field", i)
		}
	}
	ok2 := int(data["ok"].(float64))
	fail := int(data["fail"].(float64))
	if ok2+fail != 2 {
		t.Errorf("ok=%d + fail=%d != 2", ok2, fail)
	}
}

// TestHandleProxyBatchTestEmptyAddrs checks the 400 path.
func TestHandleProxyBatchTestEmptyAddrs(t *testing.T) {
	app, cookie := newAuthApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"addrs": []string{}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/batch-test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty addrs", resp.StatusCode)
	}
}

// A Bearer API token must be accepted on the script-facing endpoints.
//
// Scripts cannot hold a session cookie, so token auth is the only alternative
// to leaving these endpoints open. apitoken.Store.Validate existed for a long
// time with zero callers, meaning the documented `Authorization: Bearer upp_…`
// did nothing — this test is what keeps that from silently regressing.
func TestHandleProxySubmitAcceptsBearerToken(t *testing.T) {
	app, _ := newAuthApp(t)

	// Mint a real token through the store the middleware will consult.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := app.tokens.Create(ctx, "test-script", "proxies:write")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Plain == "" {
		t.Fatal("Create() returned no plaintext token")
	}

	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"proxies": []map[string]any{{"host": "10.1.2.3", "port": 8080, "protocol": "http"}},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Plain)
	// Deliberately no session cookie: the token alone must be sufficient.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; a valid Bearer token must authenticate "+
			"script requests without a session cookie", resp.StatusCode)
	}
	var out apiResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.Success {
		t.Fatalf("success=false: %s", out.Message)
	}
	data := out.Data.(map[string]any)
	if int(data["added"].(float64)) != 1 {
		t.Errorf("added = %v, want 1", data["added"])
	}
}

// A bogus Bearer token must be rejected, not treated as anonymous-allowed.
func TestHandleProxySubmitRejectsBadBearerToken(t *testing.T) {
	app, _ := newAuthApp(t)
	srv := httptest.NewServer(mustRouter(t, app))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"proxies": []map[string]any{{"host": "10.1.2.4", "port": 8080}},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxies/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer upp_totally_made_up")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an invalid token", resp.StatusCode)
	}
}

// The submit response must not report "added N" without saying the pool did not
// actually grow by N.
//
// Once the raw pool is at MaxRawProxies, inserting N addresses makes Trim evict
// N others. Measured against a live panel: submitting 4 addresses reported
// "added 4" while the pool total went from 4000 to 4001. A script that only
// reads `added` concludes the submission worked when it mostly displaced other
// proxies.
func TestSubmitPayloadSurfacesEvictionNotJustAdded(t *testing.T) {
	// Eviction happened: added 4, pool grew by 1, so 3 were displaced.
	res := freproxies.SubmitResult{
		Parsed: 4, Added: 4, Duplicates: 0,
		Evicted: 3, NetGrowth: 1, RawAtCap: true,
	}
	got := submitPayload(res, 5)

	if got["added"] != 4 {
		t.Errorf("added = %v, want 4", got["added"])
	}
	if got["evicted"] != int64(3) {
		t.Errorf("evicted = %v, want 3; a caller cannot see displacement otherwise", got["evicted"])
	}
	if got["net_growth"] != int64(1) {
		t.Errorf("net_growth = %v, want 1", got["net_growth"])
	}
	if got["raw_at_cap"] != true {
		t.Error("raw_at_cap should be true so the caller knows this is capacity pressure")
	}
	note, ok := got["note"].(string)
	if !ok || note == "" {
		t.Fatal("a submission that evicted proxies must carry an explanatory note")
	}
	if !strings.Contains(note, "cap") {
		t.Errorf("the note should name the cap as the cause, got: %s", note)
	}
}

// With no eviction the response stays quiet: no note, so a clean submit is not
// cluttered with warnings that do not apply.
func TestSubmitPayloadStaysQuietWithoutEviction(t *testing.T) {
	res := freproxies.SubmitResult{
		Parsed: 2, Added: 2, Duplicates: 0,
		Evicted: 0, NetGrowth: 2, RawAtCap: false,
	}
	got := submitPayload(res, 2)
	if _, hasNote := got["note"]; hasNote {
		t.Error("a submission that evicted nothing must not carry an eviction note")
	}
	if got["evicted"] != int64(0) {
		t.Errorf("evicted = %v, want 0", got["evicted"])
	}
	if got["net_growth"] != int64(2) {
		t.Errorf("net_growth = %v, want 2", got["net_growth"])
	}
}

// Duplicates must be reported separately from additions so a script re-submitting
// the same list sees "nothing new" rather than an apparent failure.
func TestSubmitPayloadReportsDuplicates(t *testing.T) {
	res := freproxies.SubmitResult{
		Parsed: 3, Added: 0, Duplicates: 3,
		Evicted: 0, NetGrowth: 0, RawAtCap: false,
	}
	got := submitPayload(res, 3)
	if got["duplicates"] != 3 {
		t.Errorf("duplicates = %v, want 3", got["duplicates"])
	}
	if got["added"] != 0 {
		t.Errorf("added = %v, want 0", got["added"])
	}
	// No eviction, so no note even though nothing was added.
	if _, hasNote := got["note"]; hasNote {
		t.Error("an all-duplicates submission is not an eviction; it must not warn about the cap")
	}
}

// SubmitRaw's own accounting must hold on a store that is not at its cap:
// parsed/added/duplicates have to agree and eviction must read zero.
func TestSubmitRawAccountingWithoutCapPressure(t *testing.T) {
	store := freproxies.NewMemoryStore()
	svc := freproxies.NewService(store, nil, nil, false)
	ctx := context.Background()

	items := []freproxies.Proxy{
		{Host: "10.9.0.1", Port: 8080},
		{Host: "10.9.0.2", Port: 8080},
		{Host: "", Port: 0}, // malformed: must be dropped before AddRaw
	}
	res, err := svc.SubmitRaw(ctx, items, "unit-test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Parsed != 2 {
		t.Errorf("parsed = %d, want 2 (the malformed entry must be dropped)", res.Parsed)
	}
	if res.Added != 2 {
		t.Errorf("added = %d, want 2", res.Added)
	}
	if res.Evicted != 0 {
		t.Errorf("evicted = %d, want 0 on an empty pool", res.Evicted)
	}
	if res.NetGrowth != 2 {
		t.Errorf("net_growth = %d, want 2", res.NetGrowth)
	}

	// Re-submitting the same list adds nothing and evicts nothing.
	res2, err := svc.SubmitRaw(ctx, items, "unit-test")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Added != 0 {
		t.Errorf("re-submit added = %d, want 0", res2.Added)
	}
	if res2.Duplicates != 2 {
		t.Errorf("re-submit duplicates = %d, want 2", res2.Duplicates)
	}
	if res2.NetGrowth != 0 {
		t.Errorf("re-submit net_growth = %d, want 0", res2.NetGrowth)
	}
}

// --- helpers -----------------------------------------------------------------

const testPwd = "testpass1234"

// emptyFS is a minimal fs.FS that satisfies the frontend embed requirement
// without needing real asset files.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) { return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist} }

// newTestApp builds an App backed by memoryStore. Auth service is nil, so
// only public (unauthenticated) routes are reachable.
func newTestApp(t *testing.T) *App {
	t.Helper()
	store := freproxies.NewMemoryStore()
	svc := freproxies.NewService(store, nil, nil, false)
	return &App{
		free:      svc,
		indexHTML: []byte("<html>"),
		frontend:  emptyFS{},
	}
}

// newAuthApp builds a fully-functional auth+free-proxy App and returns a
// valid session cookie for testPwd. Uses a temp SQLite file.
func newAuthApp(t *testing.T) (*App, *http.Cookie) {
	t.Helper()

	tempDir := t.TempDir()
	cfg := config.App{
		PanelHost:   "127.0.0.1",
		PanelPort:   7890,
		DataDir:     tempDir,
		DBPath:      filepath.Join(tempDir, "app.db"),
		RuntimeDir:  filepath.Join(tempDir, "runtime"),
		FreeValidateURL:       "https://www.gstatic.com/generate_204",
		FreeValidateTimeoutMS: 8000,
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatal(err)
	}
	dbStore, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	settingsSvc := settings.NewService(dbStore, cfg)
	hash, _ := auth.HashPassword(testPwd)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := settingsSvc.EnsureDefaults(ctx, hash); err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewService(settingsSvc, dbStore, 3600)

	freeStore := freproxies.NewMemoryStore()
	freeSvc := freproxies.NewService(freeStore, nil, nil, false)

	app := &App{
		auth:      authSvc,
		settings:  settingsSvc,
		free:      freeSvc,
		freeCfg:   cfg,
		tokens:    apitoken.New(dbStore.DB),
		indexHTML: []byte("<html>"),
		frontend:  emptyFS{},
	}

	// Login to acquire a session cookie.
	router := mustRouter(t, app)
	s := httptest.NewServer(router)
	t.Cleanup(s.Close)

	loginBody, _ := json.Marshal(map[string]string{"password": testPwd})
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/api/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == "spp_session" {
			return app, ck
		}
	}
	t.Fatal("no session cookie after login")
	return nil, nil
}

func mustRouter(t *testing.T, app *App) http.Handler {
	t.Helper()
	r, err := app.Router()
	if err != nil {
		t.Fatalf("Router(): %v", err)
	}
	return r
}

func TestParseAIProxyBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "object proxies",
			in:   `{"proxies":["1.1.1.1:80","2.2.2.2:443","1.1.1.1:80"]}`,
			want: []string{"1.1.1.1:80", "2.2.2.2:443"},
		},
		{
			name: "array strings",
			in:   `["3.3.3.3:8080","socks5://4.4.4.4:1080"]`,
			want: []string{"3.3.3.3:8080", "socks5://4.4.4.4:1080"},
		},
		{
			name: "array objects",
			in:   `[{"host":"5.5.5.5","port":3128,"protocol":"http"},{"ip":"6.6.6.6","port":"1080","proto":"socks5"}]`,
			want: []string{"5.5.5.5:3128", "socks5://6.6.6.6:1080"},
		},
		{
			name: "plain text",
			in:   "7.7.7.7:80\n# skip\n8.8.8.8:443\n",
			want: []string{"7.7.7.7:80", "8.8.8.8:443"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAIProxyBody([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d: %v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]=%q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
