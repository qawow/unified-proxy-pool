package freproxies

import (
	"context"
	"fmt"
	"testing"
	"time"

	"unified-proxy-pool/internal/crawlers"
)

// fakeChannelPolicy stands in for chanpolicy.Registry.
type fakeChannelPolicy struct {
	bans map[string]map[string]time.Time // channel -> addr -> until
}

func newFakePolicy() *fakeChannelPolicy {
	return &fakeChannelPolicy{bans: map[string]map[string]time.Time{}}
}

func (f *fakeChannelPolicy) ban(channel, addr string) {
	if f.bans[channel] == nil {
		f.bans[channel] = map[string]time.Time{}
	}
	f.bans[channel][addr] = time.Now().Add(time.Hour)
}

func (f *fakeChannelPolicy) BanSet(channel string) map[string]time.Time {
	return f.bans[channel]
}

// newPickService builds a service over a memory store with the hot cache off, so
// tests exercise the store path deterministically.
func newPickService(t *testing.T, proxies ...Proxy) *Service {
	t.Helper()
	svc := NewService(NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	svc.hot = nil
	if len(proxies) > 0 {
		ctx := context.Background()
		if _, err := svc.store.AddRaw(ctx, proxies); err != nil {
			t.Fatalf("AddRaw: %v", err)
		}
		// Validate them so they land in the scored pool the way real picks expect.
		for _, p := range proxies {
			addr := p.Addr
			if addr == "" {
				addr = fmt.Sprintf("%s:%d", p.Host, p.Port)
			}
			if err := svc.store.MarkValidated(ctx, addr, p.LatencyMS, true); err != nil {
				t.Fatalf("MarkValidated(%s): %v", addr, err)
			}
		}
	}
	return svc
}

func proxyAt(host string, port int, score float64, latency int64) Proxy {
	return Proxy{
		Host: host, Port: port, Addr: fmt.Sprintf("%s:%d", host, port),
		Protocol: "http", Score: score, LatencyMS: latency, Validated: true,
	}
}

// The headline selection requirement: a proxy banned for one channel must not be
// served to that channel, but must still be served to others.
func TestPickExcludesChannelBannedProxy(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 50, 100),
		proxyAt("10.0.0.2", 8080, 50, 100),
	)
	pol := newFakePolicy()
	pol.ban("taobao.com", "10.0.0.1:8080")
	svc.SetChannelPolicy(pol)

	for i := 0; i < 30; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Channel: "taobao.com"})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if res.Items[0].Addr == "10.0.0.1:8080" {
			t.Fatal("served a proxy banned for this channel")
		}
		if res.Relaxed {
			t.Fatal("reported Relaxed while an unbanned proxy was available")
		}
	}

	// Another channel is unaffected by that ban.
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Channel: "amazon.com"})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seen[res.Items[0].Addr] = true
	}
	if !seen["10.0.0.1:8080"] {
		t.Error("a ban on one channel suppressed the proxy on another channel")
	}
}

// Serving nothing is worse than serving a proxy the channel dislikes, but the
// caller has to be told it happened.
func TestPickRelaxesWhenChannelBannedEverything(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t, proxyAt("10.0.0.1", 8080, 50, 100))
	pol := newFakePolicy()
	pol.ban("taobao.com", "10.0.0.1:8080")
	svc.SetChannelPolicy(pol)

	res, err := svc.Pick(ctx, PickOptions{N: 1, Channel: "taobao.com"})
	if err != nil {
		t.Fatalf("Pick returned an error instead of relaxing: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("got %d proxies, want 1", len(res.Items))
	}
	if !res.Relaxed {
		t.Error("served a banned proxy without setting Relaxed; the caller cannot tell")
	}
}

func TestPickEmptyPoolStillErrors(t *testing.T) {
	svc := newPickService(t)
	if _, err := svc.Pick(context.Background(), PickOptions{N: 1}); err == nil {
		t.Error("empty pool returned no error")
	}
}

// Weighting must actually bias the draw, otherwise it is just random with extra
// arithmetic.
func TestPickWeightedFavoursHigherQuality(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 100, 50),  // best: top score, fastest
		proxyAt("10.0.0.2", 8080, 10, 3000), // worst: floor score, slow
	)

	counts := map[string]int{}
	const draws = 600
	for i := 0; i < draws; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Strategy: StrategyWeighted, NoCooldown: true})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[res.Items[0].Addr]++
	}
	good, bad := counts["10.0.0.1:8080"], counts["10.0.0.2:8080"]
	if good <= bad {
		t.Errorf("weighted pick chose the good proxy %d times vs the bad one %d; expected a clear bias", good, bad)
	}
	// The weak proxy must still be reachable — a pool where low scorers are never
	// tried can never recover them.
	if bad == 0 {
		t.Error("the low-quality proxy was never picked in 600 draws; weighting became exclusion")
	}
}

func TestPickP2CFavoursHigherQuality(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 100, 50),
		proxyAt("10.0.0.2", 8080, 10, 3000),
	)
	counts := map[string]int{}
	const draws = 200
	for i := 0; i < draws; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Strategy: StrategyP2C, NoCooldown: true})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[res.Items[0].Addr]++
	}
	if counts["10.0.0.1:8080"] <= counts["10.0.0.2:8080"] {
		t.Errorf("p2c good=%d bad=%d, expected bias to the better proxy", counts["10.0.0.1:8080"], counts["10.0.0.2:8080"])
	}
}

// Batch picks are the point of ?count=; duplicates in one response would be
// useless to a caller building a rotation.
func TestPickBatchReturnsDistinctProxies(t *testing.T) {
	ctx := context.Background()
	var proxies []Proxy
	for i := 1; i <= 10; i++ {
		proxies = append(proxies, proxyAt(fmt.Sprintf("10.0.0.%d", i), 8080, 50, 100))
	}
	svc := newPickService(t, proxies...)

	res, err := svc.Pick(ctx, PickOptions{N: 5})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("got %d proxies, want 5", len(res.Items))
	}
	seen := map[string]bool{}
	for _, p := range res.Items {
		if seen[p.Addr] {
			t.Errorf("duplicate proxy %s in one batch", p.Addr)
		}
		seen[p.Addr] = true
	}
}

// Asking for more than the pool holds returns the pool, not an error.
func TestPickBatchLargerThanPool(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 50, 100),
		proxyAt("10.0.0.2", 8080, 50, 100),
	)
	res, err := svc.Pick(ctx, PickOptions{N: 10})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Items) != 2 {
		t.Errorf("got %d proxies, want the 2 the pool holds", len(res.Items))
	}
}

// Each channel keeps its own cursor, so one channel's traffic does not skew
// another's coverage.
func TestPickRoundRobinCursorsAreIndependentPerChannel(t *testing.T) {
	ctx := context.Background()
	var proxies []Proxy
	for i := 1; i <= 4; i++ {
		proxies = append(proxies, proxyAt(fmt.Sprintf("10.0.0.%d", i), 8080, 50, 100))
	}
	svc := newPickService(t, proxies...)

	// Advance channel A three steps.
	var aSeq []string
	for i := 0; i < 3; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Strategy: StrategyRR, Channel: "a.com", NoCooldown: true})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		aSeq = append(aSeq, res.Items[0].Addr)
	}
	// Channel B starts from its own zero, so its first pick is A's first pick.
	res, err := svc.Pick(ctx, PickOptions{N: 1, Strategy: StrategyRR, Channel: "b.com", NoCooldown: true})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Items[0].Addr != aSeq[0] {
		t.Errorf("channel b started at %s, want %s — cursors are shared between channels", res.Items[0].Addr, aSeq[0])
	}
	// And A's walk covered distinct proxies rather than re-drawing the same one.
	if aSeq[0] == aSeq[1] || aSeq[1] == aSeq[2] {
		t.Errorf("round robin repeated within one channel: %v", aSeq)
	}
}

func TestPickRoundRobinWrapsAroundPool(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 50, 100),
		proxyAt("10.0.0.2", 8080, 50, 100),
	)
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, Strategy: StrategyRR, Channel: "a.com", NoCooldown: true})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		seen[res.Items[0].Addr]++
	}
	if len(seen) != 2 {
		t.Fatalf("round robin touched %d of 2 proxies", len(seen))
	}
	for addr, n := range seen {
		if n != 3 {
			t.Errorf("%s served %d times, want an even 3", addr, n)
		}
	}
}

// Cooldown spreads load instead of hammering whichever proxy scores best.
func TestPickCooldownSpreadsLoad(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 100, 50), // would otherwise dominate
		proxyAt("10.0.0.2", 8080, 60, 200),
		proxyAt("10.0.0.3", 8080, 60, 200),
	)
	svc.SetPickDefaults(StrategyWeighted, time.Minute)

	counts := map[string]int{}
	for i := 0; i < 90; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[res.Items[0].Addr]++
	}
	if len(counts) < 3 {
		t.Errorf("cooldown left %d of 3 proxies unused: %v", len(counts), counts)
	}
	// The strongest proxy should not take the overwhelming majority once it has
	// just been handed out.
	if counts["10.0.0.1:8080"] > 60 {
		t.Errorf("top proxy took %d/90 draws despite cooldown: %v", counts["10.0.0.1:8080"], counts)
	}
}

func TestPickCooldownDisabledConcentratesOnBest(t *testing.T) {
	ctx := context.Background()
	svc := newPickService(t,
		proxyAt("10.0.0.1", 8080, 100, 50),
		proxyAt("10.0.0.2", 8080, 10, 3000),
	)
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		res, err := svc.Pick(ctx, PickOptions{N: 1, NoCooldown: true})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[res.Items[0].Addr]++
	}
	// Without cooldown the weighting is free to concentrate; this is the contrast
	// case for the test above.
	if counts["10.0.0.1:8080"] <= counts["10.0.0.2:8080"] {
		t.Errorf("without cooldown the best proxy did not dominate: %v", counts)
	}
}
