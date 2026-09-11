package freproxies

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// probePlan must order the chain the way production dials it: entry mode puts
// the front first, exit mode puts it last.
func TestProbePlanHopsOrdering(t *testing.T) {
	front := Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}
	cand := Proxy{Addr: "1.2.3.4:8080", Protocol: "http"}

	entry := (&probePlan{front: front, mode: "entry"}).hops(cand)
	if len(entry) != 2 || entry[0].Addr != front.Addr || entry[1].Addr != cand.Addr {
		t.Fatalf("entry hops = %+v", entry)
	}
	exit := (&probePlan{front: front, mode: "exit"}).hops(cand)
	if len(exit) != 2 || exit[0].Addr != cand.Addr || exit[1].Addr != front.Addr {
		t.Fatalf("exit hops = %+v", exit)
	}
}

func TestFrontErrorMatchesSentinel(t *testing.T) {
	err := &FrontError{Err: errors.New("dial tcp 9.9.9.9:1080: connection refused")}
	if !errors.Is(err, ErrFrontUnavailable) {
		t.Fatal("FrontError should match ErrFrontUnavailable")
	}
	if errors.Is(errors.New("boom"), ErrFrontUnavailable) {
		t.Fatal("unrelated error must not match ErrFrontUnavailable")
	}
}

// A probe with a front plan must dial the chain (front + candidate) rather than
// the candidate directly, and the candidate's own protocol is left to the chain
// dialer (transport.Proxy stays unset).
func TestFetchThroughViaFrontPlan(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	front := Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}
	cand := Proxy{Addr: "1.2.3.4:8080", Protocol: "http"}

	var gotHops []Proxy
	var gotTarget string
	dialer := func(ctx context.Context, hops []Proxy, target string) (net.Conn, error) {
		gotHops = append([]Proxy(nil), hops...)
		gotTarget = target
		// Stand in for the chain: a direct connection to the origin.
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", target)
	}
	plan := &probePlan{front: front, mode: "entry", dial: dialer}

	_, ok, err := fetchThrough(context.Background(), cand, origin.URL, 3*time.Second, false, plan)
	if err != nil || !ok {
		t.Fatalf("fetchThrough = ok=%v err=%v", ok, err)
	}
	if len(gotHops) != 2 || gotHops[0].Addr != front.Addr || gotHops[1].Addr != cand.Addr {
		t.Fatalf("dialer saw hops %+v, want [front candidate]", gotHops)
	}
	if gotTarget != originAddr {
		t.Fatalf("dialer target = %q, want %q", gotTarget, originAddr)
	}
}

// A dial failure owned by the front node must surface as ErrFrontUnavailable,
// even after net/http wraps it in a *url.Error.
func TestCheckHTTPProxyPlanFrontFailure(t *testing.T) {
	cand := Proxy{Addr: "1.2.3.4:8080", Protocol: "http"}
	front := Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}
	dialer := func(context.Context, []Proxy, string) (net.Conn, error) {
		return nil, &FrontError{Err: errors.New("front refused")}
	}
	plan := &probePlan{front: front, mode: "entry", dial: dialer}

	// HTTPS validate URL, so a passing first fetch would need no canary round
	// trip — here nothing passes, and the failure must be the front's.
	_, ok, err := checkHTTPProxyPlan(context.Background(), cand, "https://example.com/", 3*time.Second, plan)
	if ok {
		t.Fatal("probe succeeded with a dead front")
	}
	if !errors.Is(err, ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable", err)
	}
}

// A dead front node must not cost candidates their place in the pool: the probe
// is skipped without a verdict, and repeated probes ride the cooldown instead
// of re-dialling the dead front once per candidate.
func TestTestProxyURLsFrontDownSkipsScoring(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t)
	if _, err := svc.store.AddRaw(ctx, []Proxy{{Host: "10.9.9.9", Port: 8080, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	addr := normalizeAddr("10.9.9.9", 8080)

	var dials atomic.Int64
	svc.SetProbeFront(
		func() *ProbeFront {
			return &ProbeFront{Hop: Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}, Mode: "entry"}
		},
		func(context.Context, []Proxy, string) (net.Conn, error) {
			dials.Add(1)
			return nil, &FrontError{Err: errors.New("front refused")}
		},
	)

	_, err := svc.TestProxyURLs(ctx, addr, []string{"https://example.com/"}, 2*time.Second, false)
	if !errors.Is(err, ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable", err)
	}
	stored, gerr := svc.store.Get(ctx, addr)
	if gerr != nil {
		t.Fatalf("candidate vanished from the store after a front outage: %v", gerr)
	}
	if !stored.LastCheck.IsZero() || stored.Validated {
		t.Fatalf("front outage was scored against the candidate: %+v", stored)
	}

	// The cooldown keeps the next round from re-dialling the dead front.
	if _, err := svc.TestProxyURLs(ctx, addr, []string{"https://example.com/"}, 2*time.Second, false); !errors.Is(err, ErrFrontUnavailable) {
		t.Fatalf("second probe err = %v", err)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("front was re-dialled during cooldown: %d dials, want 1", n)
	}
}

// exit_via that does not parse must fail closed for probes too, and the error
// must be the front's — not a verdict on the candidate.
func TestTestProxyURLsBrokenFrontFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t)
	if _, err := svc.store.AddRaw(ctx, []Proxy{{Host: "10.9.9.8", Port: 8080, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	addr := normalizeAddr("10.9.9.8", 8080)

	var dials atomic.Int64
	svc.SetProbeFront(
		func() *ProbeFront { return &ProbeFront{Err: errors.New(`exit_via: unsupported scheme "https"`)} },
		func(context.Context, []Proxy, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("must not dial")
		},
	)

	_, err := svc.TestProxyURLs(ctx, addr, []string{"https://example.com/"}, time.Second, false)
	if !errors.Is(err, ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable", err)
	}
	if dials.Load() != 0 {
		t.Fatal("the dialer ran with a broken front config")
	}
	stored, gerr := svc.store.Get(ctx, addr)
	if gerr != nil || !stored.LastCheck.IsZero() {
		t.Fatalf("broken front was scored against the candidate: %+v (get err %v)", stored, gerr)
	}
}

// Contrast case: with the front healthy, a failing candidate is scored exactly
// as before — a raw proxy that fails validation is deleted.
func TestTestProxyURLsCandidateFailureStillScored(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t)
	if _, err := svc.store.AddRaw(ctx, []Proxy{{Host: "10.9.9.7", Port: 8080, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	addr := normalizeAddr("10.9.9.7", 8080)

	svc.SetProbeFront(
		func() *ProbeFront {
			return &ProbeFront{Hop: Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}, Mode: "entry"}
		},
		func(context.Context, []Proxy, string) (net.Conn, error) {
			return nil, errors.New("candidate refused CONNECT")
		},
	)

	_, err := svc.TestProxyURLs(ctx, addr, []string{"https://example.com/"}, time.Second, false)
	if err == nil || errors.Is(err, ErrFrontUnavailable) {
		t.Fatalf("err = %v, want an ordinary candidate failure", err)
	}
	if _, gerr := svc.store.Get(ctx, addr); gerr == nil {
		t.Fatal("failed raw candidate should have been deleted, not kept")
	}
}

// Success path through the plan: both the validate URL and the canary are
// fetched through the chain dialer, and the candidate is promoted to validated.
func TestTestProxyURLsViaFrontSuccess(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	old := canaryURL
	canaryURL = origin.URL
	defer func() { canaryURL = old }()

	ctx := context.Background()
	svc := newPickService(t)
	if _, err := svc.store.AddRaw(ctx, []Proxy{{Host: "10.9.9.6", Port: 8080, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	addr := normalizeAddr("10.9.9.6", 8080)

	var chainDials atomic.Int64
	svc.SetProbeFront(
		func() *ProbeFront {
			return &ProbeFront{Hop: Proxy{Addr: "9.9.9.9:1080", Protocol: "socks5", Source: "exit_via"}, Mode: "entry"}
		},
		func(ctx context.Context, hops []Proxy, target string) (net.Conn, error) {
			chainDials.Add(1)
			if len(hops) != 2 || hops[1].Addr != addr {
				return nil, errors.New("chain did not end at the candidate")
			}
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", target)
		},
	)

	if _, err := svc.TestProxyURLs(ctx, addr, []string{origin.URL}, 3*time.Second, false); err != nil {
		t.Fatalf("TestProxyURLs: %v", err)
	}
	stored, gerr := svc.store.Get(ctx, addr)
	if gerr != nil || !stored.Validated {
		t.Fatalf("candidate not validated after a clean probe: %+v (get err %v)", stored, gerr)
	}
	if chainDials.Load() < 2 {
		t.Fatalf("expected validate + canary fetches through the chain, got %d dials", chainDials.Load())
	}
}
