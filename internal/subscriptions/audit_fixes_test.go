package subscriptions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/nodes"
)

// normalizeUpsertRequest stored any URL unchecked: empty, non-http(s) and
// host-less values were accepted and only failed at sync time — silently so
// under the scheduler.
func TestCreateValidatesURL(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	for _, bad := range []string{"", "ftp://example.com/sub", "javascript:alert(1)", "https://", "not-a-url"} {
		if _, err := svc.Create(ctx, UpsertRequest{Name: "bad", URL: bad, Enabled: true, SyncIntervalSec: 60}); err == nil {
			t.Fatalf("Create(url=%q) error = nil, want validation error", bad)
		}
	}
	if _, err := svc.Create(ctx, UpsertRequest{Name: "ok", URL: "https://example.com/sub", Enabled: true, SyncIntervalSec: 60}); err != nil {
		t.Fatalf("Create(valid url) error = %v", err)
	}
}

// fetch_proxy resolved to direct on ANY unparseable value — a typo like
// "driect" silently downgraded the fetch while the user believed it went
// through the pool. It must be rejected at save time.
func TestCreateValidatesFetchProxy(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	if _, err := svc.Create(ctx, UpsertRequest{Name: "typo", URL: "https://example.com/s", FetchProxy: "driect", Enabled: true, SyncIntervalSec: 60}); err == nil {
		t.Fatal("Create(fetch_proxy=driect) error = nil, want validation error")
	}
	for _, ok := range []string{"", "none", "direct", "pool", "chain", "7892", "7893", "single", "socks5://u:p@127.0.0.1:1080", "http://127.0.0.1:8080"} {
		if _, err := svc.Create(ctx, UpsertRequest{Name: "ok-" + ok, URL: "https://example.com/s", FetchProxy: ok, Enabled: true, SyncIntervalSec: 60}); err != nil {
			t.Fatalf("Create(fetch_proxy=%q) error = %v", ok, err)
		}
	}
}

// The same node listed twice in one payload produced two rows that coexisted
// with independent enabled/probe state forever.
func TestSyncDedupesPayloadDuplicates(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	uri := "vless://11111111-2222-3333-4444-555555555555@example.org:443#dup"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(uri + "\n" + uri + "\n"))
	}))
	defer server.Close()

	sub, err := svc.Create(ctx, UpsertRequest{Name: "dup-sub", URL: server.URL, Enabled: true, SyncIntervalSec: 60})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	outcome, err := svc.Sync(ctx, sub.ID)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if outcome.CreatedCount != 1 {
		t.Fatalf("CreatedCount = %d, want 1 (payload duplicates must dedup)", outcome.CreatedCount)
	}
	nodes, err := svc.ListNodes(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("stored nodes = %d, want 1", len(nodes))
	}
}

// failSync must persist the failure even when the caller's ctx is already
// cancelled — otherwise a cancelled scheduled sync leaves a stale "ok" status.
func TestFailSyncPersistsOnCancelledCtx(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	sub, err := svc.Create(ctx, UpsertRequest{Name: "sub", URL: "https://example.com/s", Enabled: true, SyncIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	svc.failSync(cancelled, sub.ID, "fetch aborted")

	got, err := svc.Get(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSyncStatus != "failed" || got.LastError != "fetch aborted" {
		t.Fatalf("status=%q error=%q, want failed/fetch aborted", got.LastSyncStatus, got.LastError)
	}
}

// doWithRetry used to sleep out the whole retry schedule on a cancelled ctx.
func TestDoWithRetryHonoursCancel(t *testing.T) {
	_, _, svc := newSubscriptionTestService(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(cancelled, http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = svc.doWithRetry(req, 5, svc.client)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("doWithRetry error = %v, want context.Canceled", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("doWithRetry slept through a cancelled ctx (%v)", time.Since(start))
	}
}

// Mixed feeds put plaintext URIs and base64 blobs on separate lines; the old
// two-stage parse returned early on the first plaintext hit and dropped the
// encoded line's nodes.
func TestParseSubscriptionMixedPlaintextAndBase64(t *testing.T) {
	plain := "trojan://password@example.com:443#plain"
	encoded := "dmxlc3M6Ly91dWlkQGV4YW1wbGUub3JnOjg0NDMjZW5jb2RlZA==" // vless://uuid@example.org:8443#encoded
	result := ParseSubscriptionContent(plain + "\n" + encoded + "\n")
	if len(result.Nodes) != 2 {
		t.Fatalf("mixed feed nodes = %d errs %v, want 2", len(result.Nodes), result.Errors)
	}
	names := map[string]bool{}
	for _, n := range result.Nodes {
		names[n.Protocol] = true
	}
	if !names["trojan"] || !names["vless"] {
		t.Fatalf("protocols = %+v, want trojan+vless", names)
	}
}

// Parse errors echo the offending line — cap it so a 1MB credential-carrying
// line cannot land in last_error or the UI.
func TestParseErrorLineIsTruncated(t *testing.T) {
	longLine := "notauri://" + strings.Repeat("x", 5000)
	_, errs := nodes.ParseRawNodes(longLine)
	if len(errs) == 0 {
		t.Fatal("expected parse error")
	}
	if len(errs[0].Error()) > 300 {
		t.Fatalf("error echoes %d bytes of the bad line, want ~120 chars", len(errs[0].Error()))
	}
}
