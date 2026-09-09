package freproxies

import (
	"context"
	"testing"

	"unified-proxy-pool/internal/crawlers"
)

func TestIsLocalAddr(t *testing.T) {
	local := []string{
		"127.0.0.1:7891", "localhost:8080", "10.1.2.3:3128", "192.168.2.198:7892",
		"172.16.5.5:8080", "169.254.169.254:80", "0.0.0.0:80", "[::1]:7891",
	}
	for _, a := range local {
		if !isLocalAddr(a) {
			t.Errorf("isLocalAddr(%q) = false, want true", a)
		}
	}
	remote := []string{"8.8.8.8:8080", "1.2.3.4:3128", "[2001:db8::1]:1080"}
	for _, a := range remote {
		if isLocalAddr(a) {
			t.Errorf("isLocalAddr(%q) = true, want false", a)
		}
	}
}

// Submitting must not let the caller declare its own proxy healthy, and must
// not accept the pool's own listener (which would loop back into itself). A
// plain LAN proxy stays allowed: pushing a locally-run clash instance into the
// pool is a supported workflow.
func TestSubmitRawSanitizesUntrustedFields(t *testing.T) {
	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	ctx := context.Background()
	SetSelfPorts(7891, 7892, 7893)
	t.Cleanup(func() { SetSelfPorts() })

	res, err := svc.SubmitRaw(ctx, []Proxy{
		{Host: "8.8.8.8", Port: 8080, Validated: true, Score: ScoreMax, Region: "US"},
		{Host: "127.0.0.1", Port: 7896}, // local clash: allowed
		{Host: "127.0.0.1", Port: 7892}, // our own single-hop listener: rejected
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Fatalf("added = %d, want 2", res.Added)
	}
	got, err := svc.Store().Get(ctx, "8.8.8.8:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got.Validated {
		t.Error("submitted proxy was stored as validated without ever being probed")
	}
	if got.Score != ScoreInit {
		t.Errorf("score = %v, want ScoreInit (%v)", got.Score, ScoreInit)
	}
	if _, err := svc.Store().Get(ctx, "127.0.0.1:7892"); err == nil {
		t.Error("the pool's own listener made it into the pool")
	}
	if _, err := svc.Store().Get(ctx, "127.0.0.1:7896"); err != nil {
		t.Error("a locally-run proxy should still be submittable")
	}
}
