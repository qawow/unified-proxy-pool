package chanpolicy

import (
	"testing"
	"time"
)

func banNow(t *testing.T, r *Registry, channel, addr string) *Ban {
	t.Helper()
	b := r.Record(Outcome{Channel: channel, Addr: addr, Status: 403})
	if b == nil {
		t.Fatalf("expected a ban for %s/%s", channel, addr)
	}
	return b
}

// A temporary ban has to actually be temporary.
func TestBanExpiresOnItsOwn(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 60 })
	banNow(t, r, "ch", "1.1.1.1:80")
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Fatal("not banned right after banning")
	}
	clk.advance(59 * time.Second)
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Error("released one second early")
	}
	clk.advance(2 * time.Second)
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("still banned after the TTL elapsed")
	}
}

// Repeat offenders get progressively longer bans, capped.
func TestBanTTLEscalatesAndCaps(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.BanTTLSec = 60
		p.BanTTLMaxSec = 240
	})
	want := []int{60, 120, 240, 240}
	for i, wantTTL := range want {
		b := banNow(t, r, "ch", "1.1.1.1:80")
		if b.TTLSec != wantTTL {
			t.Errorf("strike %d: TTL = %ds, want %ds", i+1, b.TTLSec, wantTTL)
		}
		if b.Strikes != i+1 {
			t.Errorf("strike %d: Strikes = %d", i+1, b.Strikes)
		}
		// Walk past expiry so the next failure can ban again.
		clk.advance(time.Duration(wantTTL)*time.Second + time.Second)
	}
}

// Without this, a proxy that misbehaves once an hour would ratchet up to the
// maximum ban and never come back down.
func TestBanLadderResetsAfterQuietPeriod(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.BanTTLSec = 60
		p.BanTTLMaxSec = 1800
	})
	b := banNow(t, r, "ch", "1.1.1.1:80")
	if b.TTLSec != 60 {
		t.Fatalf("first ban TTL = %d, want 60", b.TTLSec)
	}
	clk.advance(70 * time.Second)
	b = banNow(t, r, "ch", "1.1.1.1:80")
	if b.TTLSec != 120 {
		t.Fatalf("second ban TTL = %d, want 120 (escalated)", b.TTLSec)
	}
	// Stay quiet well past the idle reset, then offend again.
	clk.advance(r.Policy().idleResetAfter() + time.Minute)
	b = banNow(t, r, "ch", "1.1.1.1:80")
	if b.TTLSec != 60 {
		t.Errorf("TTL after a quiet period = %ds, want 60 (ladder should reset)", b.TTLSec)
	}
}

// An already-banned pair must not stack strikes: it is excluded from selection,
// so any further failures are stale in-flight requests, not new evidence.
func TestFailuresWhileBannedDoNotStackStrikes(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 60 })
	first := banNow(t, r, "ch", "1.1.1.1:80")
	for i := 0; i < 5; i++ {
		if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 403}); b != nil {
			t.Fatalf("re-banned while already banned: %+v", b)
		}
	}
	bans := r.Bans("ch")
	if len(bans) != 1 {
		t.Fatalf("got %d bans, want 1", len(bans))
	}
	if bans[0].Strikes != first.Strikes {
		t.Errorf("strikes grew to %d while banned, want %d", bans[0].Strikes, first.Strikes)
	}
}

// Old failures have to fade out, otherwise a proxy that failed this morning still
// carries the evidence tonight. This is the specific flaw in the cumulative
// counters that sourcestats uses.
func TestWindowedFailuresDecay(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.WindowSec = 300
		p.ConsecutiveFails = 0
		p.TimeoutFails = 0
		p.FailRate = 0.6
		p.MinSamples = 4
	})
	for i := 0; i < 3; i++ {
		fail(r, "ch", "1.1.1.1:80")
	}
	// Walk the whole window forward so those 3 failures age out entirely.
	clk.advance(310 * time.Second)
	for i := 0; i < 3; i++ {
		ok(r, "ch", "1.1.1.1:80")
	}
	if b := fail(r, "ch", "1.1.1.1:80"); b != nil {
		t.Errorf("banned on evidence that should have aged out: %+v", b)
	}
	st := findChannel(t, r, "ch")
	if st.Fail != 1 {
		t.Errorf("Fail = %d, want 1 (the three old failures must have decayed)", st.Fail)
	}
}

func findChannel(t *testing.T, r *Registry, name string) ChannelStat {
	t.Helper()
	for _, st := range r.Channels() {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("channel %q not found", name)
	return ChannelStat{}
}
