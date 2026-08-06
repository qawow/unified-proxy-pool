package main

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// The confidence interval is the whole reason this tool samples instead of
// guessing, so it has to be honest at the small sample sizes that actually occur.
func TestRateCIWidensAtSmallSamples(t *testing.T) {
	small := yield{sampled: 5, alive: 1}
	large := yield{sampled: 500, alive: 100}

	// Same observed rate, very different confidence.
	if small.rate() != large.rate() {
		t.Fatalf("fixture error: rates differ (%v vs %v)", small.rate(), large.rate())
	}
	sLow, sHigh := small.rateCI()
	lLow, lHigh := large.rateCI()

	if (sHigh - sLow) <= (lHigh - lLow) {
		t.Errorf("5 samples must be less certain than 500: [%.3f,%.3f] vs [%.3f,%.3f]",
			sLow, sHigh, lLow, lHigh)
	}
	for _, c := range []struct {
		name      string
		low, high float64
	}{{"small", sLow, sHigh}, {"large", lLow, lHigh}} {
		if c.low < 0 || c.high > 1 || c.low > c.high {
			t.Errorf("%s interval is not a valid probability range: [%.3f,%.3f]", c.name, c.low, c.high)
		}
	}
}

// Zero alive out of a handful is not evidence of a 0% rate. The upper bound has
// to stay high enough that the report does not overclaim.
func TestRateCIZeroAliveKeepsUpperBound(t *testing.T) {
	few := yield{sampled: 5, alive: 0}
	many := yield{sampled: 400, alive: 0}

	_, fewHigh := few.rateCI()
	_, manyHigh := many.rateCI()

	if fewHigh <= manyHigh {
		t.Errorf("0/5 must admit a higher true rate than 0/400: %.3f vs %.3f", fewHigh, manyHigh)
	}
	if fewHigh < 0.2 {
		t.Errorf("0/5 claims the rate is under %.1f%%, which 5 dials cannot establish", fewHigh*100)
	}
}

// A source with a small sample must not receive a verdict: that is how a
// transient network blip turns into a permanently disabled source.
func TestAdviceWithholdsVerdictOnTinySample(t *testing.T) {
	y := yield{name: "tiny", fetched: 8, sampled: 8, alive: 0}
	action, why := y.advice(20)
	if action != "UNKNOWN" {
		t.Errorf("expected UNKNOWN for an 8-address sample, got %s (%s)", action, why)
	}
	if !strings.Contains(why, "need 20") {
		t.Errorf("the reason must state what would be enough, got: %s", why)
	}
}

// Measured case: prxchk-http published 58 addresses, none of which worked.
func TestAdviceDisablesDeadSource(t *testing.T) {
	y := yield{name: "prxchk-http", fetched: 58, sampled: 58, alive: 0}
	action, why := y.advice(20)
	if action != "DISABLE" {
		t.Errorf("a source with 0/58 alive must be DISABLE, got %s", action)
	}
	if !strings.Contains(why, "0/58") {
		t.Errorf("the reason must cite the measurement, got: %s", why)
	}
}

// Measured case: b4rc0de-socks5 published 257 addresses at ~16% alive.
func TestAdviceKeepsProductiveSource(t *testing.T) {
	y := yield{name: "b4rc0de-socks5", fetched: 257, sampled: 120, alive: 20}
	action, why := y.advice(20)
	if action != "KEEP" {
		t.Errorf("a source at ~17%% alive must be KEEP, got %s (%s)", action, why)
	}
	if !strings.Contains(why, "per round") {
		t.Errorf("the reason should project the per-round contribution, got: %s", why)
	}
}

// A productive source can still be a problem: one round over the raw cap evicts
// other sources' proxies, which is why solispirit-http ships disabled.
func TestAdviceFlagsOversizedSourceEvenWhenAlive(t *testing.T) {
	y := yield{name: "solispirit-http", fetched: 123010, sampled: 120, alive: 30}
	action, why := y.advice(20)
	if action != "OVERSIZED" {
		t.Errorf("a source %dx the raw cap must be flagged, got %s", y.fetched/freproxies.MaxRawProxies, action)
	}
	if !strings.Contains(why, "raw cap") {
		t.Errorf("the reason must name the cap, got: %s", why)
	}
}

// A fetch failure must not be reported as a measured 0% rate.
func TestAdviceDistinguishesFetchFailureFromDeadProxies(t *testing.T) {
	y := yield{name: "gone", fetchErr: errFetch("http 404")}
	action, why := y.advice(20)
	if action != "DISABLE" {
		t.Errorf("expected DISABLE, got %s", action)
	}
	if !strings.Contains(why, "fetch failed") {
		t.Errorf("the reason must distinguish a fetch failure from dead proxies, got: %s", why)
	}
}

// estimatedAlive is what the report ranks on, so it has to project the sampled
// rate onto the full output rather than report the sample count.
func TestEstimatedAliveProjectsOntoFullOutput(t *testing.T) {
	// 25% of 1000 published, measured over 200 dials.
	y := yield{fetched: 1000, sampled: 200, alive: 50}
	if got := y.estimatedAlive(); got != 250 {
		t.Errorf("estimatedAlive() = %.0f, want 250", got)
	}

	// A big list with a dead sample must rank below a small productive one.
	big := yield{fetched: 45000, sampled: 200, alive: 0}
	small := yield{fetched: 100, sampled: 100, alive: 40}
	if big.estimatedAlive() >= small.estimatedAlive() {
		t.Errorf("45k dead addresses must rank below 40 working ones: %.0f vs %.0f",
			big.estimatedAlive(), small.estimatedAlive())
	}
}

// A rate of zero must not divide by zero anywhere.
func TestZeroSampleIsSafe(t *testing.T) {
	y := yield{fetched: 10, sampled: 0, alive: 0}
	if r := y.rate(); r != 0 {
		t.Errorf("rate() on an empty sample = %v, want 0", r)
	}
	if low, high := y.rateCI(); low != 0 || high != 0 {
		t.Errorf("rateCI() on an empty sample = [%v,%v], want [0,0]", low, high)
	}
	if e := y.estimatedAlive(); e != 0 {
		t.Errorf("estimatedAlive() on an empty sample = %v, want 0", e)
	}
}

// Sampling must cover the whole list: these files are often ordered, so taking
// the head would measure a biased slice.
func TestPickSampleDrawsFromAcrossTheList(t *testing.T) {
	items := make([]crawlers.Proxy, 1000)
	for i := range items {
		items[i] = crawlers.Proxy{Host: "10.0.0.1", Port: 1000 + i}
	}
	got := pickSample(items, 100, rand.New(rand.NewSource(42)))
	if len(got) != 100 {
		t.Fatalf("expected 100 samples, got %d", len(got))
	}
	// With a uniform draw of 100 from 1000, landing entirely in the first decile
	// is astronomically unlikely; the head-slice bug would do exactly that.
	var beyondHead int
	for _, p := range got {
		if p.Port >= 1100 {
			beyondHead++
		}
	}
	if beyondHead == 0 {
		t.Error("every sample came from the first 100 entries; the draw is not random")
	}
}

// Asking for more than the list holds must return the list, not pad or panic.
func TestPickSampleHandlesShortList(t *testing.T) {
	items := []crawlers.Proxy{{Host: "10.0.0.1", Port: 1}, {Host: "10.0.0.2", Port: 2}}
	got := pickSample(items, 100, rand.New(rand.NewSource(1)))
	if len(got) != 2 {
		t.Errorf("expected all 2 items, got %d", len(got))
	}
}

type errFetch string

func (e errFetch) Error() string { return string(e) }

// TestPersistSkipsFetchErrors ensures sources that failed to fetch are not
// recorded — no measurement occurred, so there is nothing meaningful to store.
func TestPersistSkipsFetchErrors(t *testing.T) {
	store := freproxies.NewMemoryStore()
	results := []yield{
		{name: "good", fetched: 100, sampled: 50, alive: 10},
		{name: "failed", fetchErr: errFetch("timeout")},
	}
	ctx := context.Background()
	records := yieldRecords(results, 20, time.Now().UTC())
	if err := persistResults(ctx, store, records); err != nil {
		t.Fatal(err)
	}

	summary, err := store.AllSourceYieldSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := summary["failed"]; ok {
		t.Error("a source that failed to fetch should not have a yield record: " +
			"recording it as 0 alive makes an unreachable source look permanently dead")
	}
	if _, ok := summary["good"]; !ok {
		t.Error("a source that fetched successfully should have a yield record")
	}
}

// TestPersistUsesAdvice verifies the stored record carries the KEEP/DISABLE
// verdict, not just raw counts, so a report can be read without recomputing it.
func TestPersistUsesAdvice(t *testing.T) {
	store := freproxies.NewMemoryStore()
	ctx := context.Background()
	results := []yield{{name: "dead", fetched: 58, sampled: 58, alive: 0}}

	records := yieldRecords(results, 20, time.Now().UTC())
	if err := persistResults(ctx, store, records); err != nil {
		t.Fatal(err)
	}

	summary, err := store.AllSourceYieldSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Assert unconditionally. The earlier version nested these behind
	// `if err == nil`, so a failing call skipped every assertion and the test
	// passed while checking nothing.
	rec, ok := summary["dead"]
	if !ok {
		t.Fatal("the measured source is missing from the summary")
	}
	if rec.Action != "DISABLE" {
		t.Errorf("0/58 alive should get Action=DISABLE, got %q", rec.Action)
	}
	if rec.Why == "" {
		t.Error("Why should explain the verdict")
	}
}

// An empty -redis address must be refused, not defaulted.
//
// go-redis maps an empty Addr to localhost:6379 db 0 (options.go), which is the
// live pool. An earlier version of this test suite called the persist path with
// "" and wrote fixture rows named "dead" and "good" into production.
func TestOpenYieldStoreRefusesEmptyAddress(t *testing.T) {
	store, err := openYieldStore("", "", 0)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("an empty Redis address must be rejected; go-redis would silently " +
			"substitute localhost:6379 db 0, which is the live pool")
	}
	if !strings.Contains(err.Error(), "live pool") {
		t.Errorf("the error should say why the default is dangerous, got: %v", err)
	}
}

// yieldRecords is the pure half of the persist path: it must decide what gets
// written without any connection, so tests never need a Redis to check it.
func TestYieldRecordsIsPure(t *testing.T) {
	now := time.Now().UTC()
	results := []yield{
		{name: "alpha", fetched: 1000, sampled: 200, alive: 50},
		{name: "broken", fetchErr: errFetch("http 404")},
	}
	got := yieldRecords(results, 20, now)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (the fetch failure dropped), got %d", len(got))
	}
	if got[0].Source != "alpha" {
		t.Errorf("wrong source recorded: %q", got[0].Source)
	}
	// Estimate must project the sample onto full output, which is what a report
	// ranks on; storing the sample count would understate a large source.
	if got[0].Estimate != 250 {
		t.Errorf("Estimate = %d, want 250 (25%% of 1000)", got[0].Estimate)
	}
	if !got[0].MeasureAt.Equal(now) {
		t.Errorf("MeasureAt should be the passed timestamp, got %v", got[0].MeasureAt)
	}
}

// TestSourceYieldRecordRate checks the convenience method matches the raw ratio.
func TestSourceYieldRecordRate(t *testing.T) {
	rec := freproxies.SourceYieldRecord{Sampled: 100, Alive: 23}
	if got := rec.Rate(); got != 0.23 {
		t.Errorf("expected 0.23, got %f", got)
	}
	zero := freproxies.SourceYieldRecord{Sampled: 0, Alive: 0}
	if got := zero.Rate(); got != 0 {
		t.Errorf("zero sample should return 0, got %f", got)
	}
}

// TestTrendDetection verifies the three-tier classification: improving, stable, degrading.
func TestTrendDetection(t *testing.T) {
	store := freproxies.NewMemoryStore()
	ctx := context.Background()

	// Simulate a source improving over 6 measurements.
	base := time.Now().UTC()
	for i := 0; i < 6; i++ {
		rec := freproxies.SourceYieldRecord{
			Source:    "uptrend",
			MeasureAt: base.Add(-time.Duration(5-i) * time.Hour),
			Fetched:   100,
			Sampled:   100,
			Alive:     10 + i*5, // 10, 15, 20, 25, 30, 35 → clear uptrend
		}
		_ = store.SaveSourceYield(ctx, rec)
	}

	trend, records, err := store.SourceYieldTrend(ctx, "uptrend", 6)
	if err != nil {
		t.Fatal(err)
	}
	if trend != "IMPROVING" {
		t.Errorf("expected IMPROVING, got %s", trend)
	}
	if len(records) != 6 {
		t.Errorf("expected 6 records, got %d", len(records))
	}
	// Newest should be first.
	if records[0].Alive != 35 {
		t.Errorf("newest record should have alive=35, got %d", records[0].Alive)
	}
}

// TestTrendInsufficientData checks that trends cannot be computed from fewer
// than 2 measurements.
func TestTrendInsufficientData(t *testing.T) {
	store := freproxies.NewMemoryStore()
	ctx := context.Background()

	rec := freproxies.SourceYieldRecord{
		Source: "lonely", MeasureAt: time.Now().UTC(), Fetched: 100, Sampled: 50, Alive: 10,
	}
	_ = store.SaveSourceYield(ctx, rec)

	trend, records, err := store.SourceYieldTrend(ctx, "lonely", 5)
	if err != nil {
		t.Fatal(err)
	}
	if trend != "INSUFFICIENT" {
		t.Errorf("1 record should return INSUFFICIENT, got %s", trend)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

// TestTrendStable verifies sources with consistent rates get STABLE verdict.
func TestTrendStable(t *testing.T) {
	store := freproxies.NewMemoryStore()
	ctx := context.Background()
	base := time.Now().UTC()

	for i := 0; i < 4; i++ {
		rec := freproxies.SourceYieldRecord{
			Source:    "flat",
			MeasureAt: base.Add(-time.Duration(3-i) * time.Hour),
			Fetched:   100,
			Sampled:   100,
			Alive:     20, // flat 20%
		}
		_ = store.SaveSourceYield(ctx, rec)
	}

	trend, _, err := store.SourceYieldTrend(ctx, "flat", 4)
	if err != nil {
		t.Fatal(err)
	}
	if trend != "STABLE" {
		t.Errorf("expected STABLE, got %s", trend)
	}
}

// TestTrendDegrading checks a source whose rate drops over time.
func TestTrendDegrading(t *testing.T) {
	store := freproxies.NewMemoryStore()
	ctx := context.Background()
	base := time.Now().UTC()

	for i := 0; i < 6; i++ {
		rec := freproxies.SourceYieldRecord{
			Source:    "downtrend",
			MeasureAt: base.Add(-time.Duration(5-i) * time.Hour),
			Fetched:   100,
			Sampled:   100,
			Alive:     35 - i*5, // 35, 30, 25, 20, 15, 10 → clear downtrend
		}
		_ = store.SaveSourceYield(ctx, rec)
	}

	trend, _, err := store.SourceYieldTrend(ctx, "downtrend", 6)
	if err != nil {
		t.Fatal(err)
	}
	if trend != "DEGRADING" {
		t.Errorf("expected DEGRADING, got %s", trend)
	}
}
