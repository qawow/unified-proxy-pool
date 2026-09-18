package subscriptions

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// insertSubNode writes a subscription_nodes row directly — faster than a
// round-trip sync when only the stored state matters.
func insertSubNode(t *testing.T, svc *Service, subID int64) int64 {
	t.Helper()
	res, err := svc.store.DB.Exec(`INSERT INTO subscription_nodes
		(subscription_id, display_name, protocol, server, port, raw_payload, normalized_json, enabled, last_status, created_at, updated_at)
		VALUES (?, 'n1', 'vless', 'example.org', 443, 'vless://x@example.org:443', '{}', 1, 'unknown', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, subID)
	if err != nil {
		t.Fatalf("insert node error = %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// Disabling a subscription must take its nodes offline: NodeBySource reports
// Enabled=false (so pool publish skips them while keeping membership), and
// the runtime inventory / pool candidates drop them entirely.
func TestDisabledSubscriptionTakesNodesOffline(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	sub, err := svc.Create(ctx, UpsertRequest{Name: "s", URL: "https://example.com/feed", Enabled: true, SyncIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	nodeID := insertSubNode(t, svc, sub.ID)

	n, err := svc.NodeBySource(ctx, nodeID)
	if err != nil || !n.Enabled {
		t.Fatalf("enabled sub: NodeBySource enabled=%v err=%v", n.Enabled, err)
	}
	if got, _ := svc.AllRuntimeNodes(ctx); len(got) != 1 {
		t.Fatalf("enabled sub: AllRuntimeNodes = %d, want 1", len(got))
	}
	if got, _ := svc.ListPoolCandidates(ctx); len(got) != 1 {
		t.Fatalf("enabled sub: ListPoolCandidates = %d, want 1", len(got))
	}

	if _, err := svc.Toggle(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	n, err = svc.NodeBySource(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Enabled {
		t.Fatal("disabled sub: node must report Enabled=false so publish drops it")
	}
	if got, _ := svc.AllRuntimeNodes(ctx); len(got) != 0 {
		t.Fatalf("disabled sub: AllRuntimeNodes = %d, want 0", len(got))
	}
	if got, _ := svc.ListPoolCandidates(ctx); len(got) != 0 {
		t.Fatalf("disabled sub: ListPoolCandidates = %d, want 0", len(got))
	}

	// Membership and rows survive; re-enabling restores everything.
	if _, err := svc.Toggle(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.NodeBySource(ctx, nodeID); !n.Enabled {
		t.Fatal("re-enabled sub: node should be publishable again")
	}
}

// KickSync returns immediately, rejects a second kick while running, and the
// background goroutine still records the outcome.
func TestKickSyncIsAsync(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://11111111-2222-3333-4444-555555555555@example.org:443#n\n"))
	}))
	defer server.Close()

	sub, err := svc.Create(ctx, UpsertRequest{Name: "s", URL: server.URL, Enabled: true, SyncIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.KickSync(ctx, sub.ID); err != nil {
		t.Fatalf("KickSync error = %v", err)
	}
	// beginSync is held before KickSync returns, so a second kick is always
	// refused — no timing dependency.
	if err := svc.KickSync(ctx, sub.ID); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("second KickSync error = %v, want ErrSyncRunning", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := svc.Get(ctx, sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Syncing && got.LastSyncStatus != "" {
			if got.LastSyncStatus != "ok" && got.LastSyncStatus != "not_modified" {
				t.Fatalf("last_sync_status = %q, want ok", got.LastSyncStatus)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background sync never finished; status=%q syncing=%v", got.LastSyncStatus, got.Syncing)
		}
		time.Sleep(50 * time.Millisecond)
	}
	nodes, err := svc.ListNodes(ctx, sub.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes = %d err=%v, want 1", len(nodes), err)
	}
}

// fetch_proxy aliases must resolve live: after a chain rebind the resolver
// reports the new listen address, not the startup-frozen one.
func TestFetchProxyAliasesResolveLive(t *testing.T) {
	_, _, svc := newSubscriptionTestService(t)

	addr := "127.0.0.1:4101"
	svc.SetExitResolver(func() (string, string) { return addr, "127.0.0.1:4102" })

	if got := svc.resolveFetchProxy("direct"); got == nil || got.String() != "http://127.0.0.1:4101" {
		t.Fatalf("direct alias = %v, want http://127.0.0.1:4101", got)
	}
	if got := svc.resolveFetchProxy("chain"); got == nil || got.String() != "http://127.0.0.1:4102" {
		t.Fatalf("chain alias = %v, want http://127.0.0.1:4102", got)
	}

	addr = "127.0.0.1:5101" // simulated rebind
	if got := svc.resolveFetchProxy("7892"); got == nil || got.String() != "http://127.0.0.1:5101" {
		t.Fatalf("rebound direct alias = %v, want http://127.0.0.1:5101", got)
	}
}
