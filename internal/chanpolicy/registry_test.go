package chanpolicy

import (
	"testing"
	"time"
)

// fakeClock drives every time-dependent path so the tests never sleep.
type fakeClock struct{ t time.Time }

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestRegistry(t *testing.T, tweak func(*Policy)) (*Registry, *fakeClock) {
	t.Helper()
	clk := newClock()
	p := Defaults()
	if tweak != nil {
		tweak(&p)
	}
	return New(Options{Policy: p, Now: clk.now}), clk
}

func fail(r *Registry, channel, addr string) *Ban {
	return r.Record(Outcome{Channel: channel, Addr: addr, OK: false, Err: "dial_failed"})
}

func ok(r *Registry, channel, addr string) *Ban {
	return r.Record(Outcome{Channel: channel, Addr: addr, OK: true, LatencyMS: 100})
}

// The headline requirement: a ban must not leak across channels.
func TestBanIsScopedToOneChannel(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	for i := 0; i < 3; i++ {
		fail(r, "taobao.com", "1.2.3.4:8080")
	}
	if !r.Banned("taobao.com", "1.2.3.4:8080") {
		t.Fatal("proxy should be banned on the channel that kept failing")
	}
	if r.Banned("amazon.com", "1.2.3.4:8080") {
		t.Error("ban leaked to another channel; per-channel isolation is the whole feature")
	}
	if r.Banned("taobao.com", "9.9.9.9:80") {
		t.Error("ban leaked to a different proxy on the same channel")
	}
}

func TestConsecutiveFailuresTripAtThreshold(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.FailRate = 0 // isolate the consecutive rule
		p.TimeoutFails = 0
	})
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Fatal("banned after 1 failure, threshold is 3")
	}
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Fatal("banned after 2 failures, threshold is 3")
	}
	b := fail(r, "ch", "1.1.1.1:80")
	if b == nil {
		t.Fatal("not banned after 3 consecutive failures")
	}
	if b.Reason != "consecutive_fails" {
		t.Errorf("reason = %q, want consecutive_fails", b.Reason)
	}
}

// A success in the middle breaks the streak, otherwise a proxy that works most of
// the time would still accumulate its way to a ban.
func TestSuccessResetsConsecutiveStreak(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.FailRate = 0
		p.TimeoutFails = 0
	})
	fail(r, "ch", "1.1.1.1:80")
	fail(r, "ch", "1.1.1.1:80")
	ok(r, "ch", "1.1.1.1:80")
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Error("a success did not break the failure streak")
	}
}

// Two failures far apart are not a run of two.
func TestStaleFailureDoesNotContinueStreak(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.FailRate = 0
		p.TimeoutFails = 0
	})
	fail(r, "ch", "1.1.1.1:80")
	fail(r, "ch", "1.1.1.1:80")
	clk.advance(time.Duration(Defaults().WindowSec)*time.Second + time.Minute)
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Error("failures separated by more than the window were counted as consecutive")
	}
}

func TestBanStatusTripsOnFirstSighting(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", OK: false, Status: 403})
	if b == nil {
		t.Fatal("403 did not ban immediately; it is the clearest anti-scrape signal there is")
	}
	if b.Reason != "status_403" {
		t.Errorf("reason = %q, want status_403", b.Reason)
	}
}

func TestNonListedStatusDoesNotTripImmediately(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", OK: false, Status: 500}); b != nil {
		t.Errorf("500 banned on first sighting; only %v should", Defaults().BanStatuses)
	}
}

// A 200 that the caller reports as ok must never ban, even for a listed status.
func TestSuccessNeverBans(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	for i := 0; i < 20; i++ {
		if b := ok(r, "ch", "1.1.1.1:80"); b != nil {
			t.Fatalf("successful outcome produced a ban: %+v", b)
		}
	}
}

func TestFailRateNeedsMinSamples(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.ConsecutiveFails = 0 // isolate the rate rule
		p.TimeoutFails = 0
		p.FailRate = 0.6
		p.MinSamples = 5
	})
	// 2 fails / 2 samples = 100% rate but below MinSamples.
	fail(r, "ch", "1.1.1.1:80")
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Fatal("banned on 2 samples; MinSamples is 5, so a short burst must not count")
	}
	ok(r, "ch", "1.1.1.1:80")
	fail(r, "ch", "1.1.1.1:80")
	// 5th sample: 3 fail / 5 total = 0.6 >= 0.6.
	if b := fail(r, "ch", "1.1.1.1:80"); b == nil {
		t.Fatal("not banned at 3/5 failures with threshold 0.6")
	}
}

func TestTimeoutsHaveTheirOwnThreshold(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.ConsecutiveFails = 0
		p.FailRate = 0
		p.TimeoutFails = 5
	})
	for i := 0; i < 4; i++ {
		if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Err: "i/o timeout"}); b != nil {
			t.Fatalf("banned after %d timeouts, threshold is 5", i+1)
		}
	}
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Err: "i/o timeout"})
	if b == nil {
		t.Fatal("not banned after 5 timeouts")
	}
	if b.Reason != "timeouts" {
		t.Errorf("reason = %q, want timeouts", b.Reason)
	}
}

func TestDisabledPolicyRecordsNothing(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.Enabled = false })
	for i := 0; i < 10; i++ {
		if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
			t.Fatal("banned while the policy is disabled")
		}
	}
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("Banned reported true while the policy is disabled")
	}
}

func TestReportedOutcomeIsMarkedInReason(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 429, Reported: true})
	if b == nil {
		t.Fatal("reported 429 did not ban")
	}
	if b.Reason != "status_429_reported" {
		t.Errorf("reason = %q, want status_429_reported so the origin stays visible", b.Reason)
	}
}
