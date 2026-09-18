package subscriptions

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAfterSyncHookSurvivesRequestCompletion(t *testing.T) {
	ctx, _, svc := newSubscriptionTestService(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "trojan://password@demo.example.com:443#demo")
	}))
	defer origin.Close()
	sub, err := svc.Create(ctx, UpsertRequest{Name: "hook-test", URL: origin.URL, Enabled: true, SyncIntervalSec: 3600})
	if err != nil {
		t.Fatal(err)
	}
	type contextKey struct{}
	requestCtx, cancel := context.WithCancel(context.WithValue(ctx, contextKey{}, "trace-id"))
	defer cancel()
	release := make(chan struct{})
	result := make(chan error, 1)
	svc.SetAfterSyncHook(func(hookCtx context.Context, id int64, nodes []int64) {
		<-release
		if _, ok := hookCtx.Deadline(); !ok {
			result <- fmt.Errorf("hook has no deadline")
			return
		}
		if hookCtx.Value(contextKey{}) != "trace-id" {
			result <- fmt.Errorf("request values lost")
			return
		}
		if len(nodes) != 1 {
			result <- fmt.Errorf("got %d nodes", len(nodes))
			return
		}
		_, err := svc.Get(hookCtx, id)
		result <- err
	})
	_, syncErr := svc.Sync(requestCtx, sub.ID)
	cancel() // the HTTP handler has returned
	close(release)
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("committed sync's hook failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("after-sync hook did not finish")
	}
}
