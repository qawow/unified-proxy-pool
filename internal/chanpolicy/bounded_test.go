package chanpolicy

import (
	"fmt"
	"testing"
	"time"
)

// Channels are keyed by destination, so their count is driven by traffic rather
// than by anything the operator sets. The map has to stay bounded.
func TestChannelCountStaysUnderCap(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) { p.MaxChannels = 10 })
	for i := 0; i < 100; i++ {
		fail(r, fmt.Sprintf("site%d.com", i), "1.1.1.1:80")
		clk.advance(time.Second)
	}
	if got := len(r.Channels()); got > 10 {
		t.Errorf("tracking %d channels, cap is 10", got)
	}
}

func TestEntriesPerChannelStayUnderCap(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) { p.MaxEntriesPerChan = 10 })
	for i := 0; i < 100; i++ {
		fail(r, "ch", fmt.Sprintf("10.0.0.%d:80", i))
		clk.advance(time.Second)
	}
	st := findChannel(t, r, "ch")
	if st.Entries > 10 {
		t.Errorf("channel holds %d entries, cap is 10", st.Entries)
	}
}

// Eviction must not silently release a live ban when an unbanned entry is
// available to drop instead — that would put a known-bad proxy back in rotation.
func TestEvictionPrefersUnbannedEntries(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.MaxEntriesPerChan = 3
		p.BanTTLSec = 3600
	})
	// One banned entry, created first so it is also the stalest.
	banNow(t, r, "ch", "1.1.1.1:80")
	clk.advance(time.Second)
	// Fill the rest with entries that carry a single success (no ban).
	ok(r, "ch", "2.2.2.2:80")
	clk.advance(time.Second)
	ok(r, "ch", "3.3.3.3:80")
	clk.advance(time.Second)
	// This insert is over the cap and must evict one of the clean entries.
	ok(r, "ch", "4.4.4.4:80")

	if !r.Banned("ch", "1.1.1.1:80") {
		t.Error("eviction dropped the banned entry and released a known-bad proxy")
	}
}

func TestEvictionPrefersChannelsWithoutBans(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.MaxChannels = 3
		p.BanTTLSec = 3600
	})
	banNow(t, r, "banned.com", "1.1.1.1:80")
	clk.advance(time.Second)
	ok(r, "clean1.com", "2.2.2.2:80")
	clk.advance(time.Second)
	ok(r, "clean2.com", "3.3.3.3:80")
	clk.advance(time.Second)
	ok(r, "new.com", "4.4.4.4:80")

	if !r.Banned("banned.com", "1.1.1.1:80") {
		t.Error("evicted the channel holding a live ban while clean channels were available")
	}
}

// Sweep is what returns memory when traffic quiets down; LRU pressure alone would
// hold the peak footprint forever.
func TestSweepDropsExpiredAndIdleState(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 60 })
	banNow(t, r, "ch", "1.1.1.1:80")
	clk.advance(70 * time.Second)
	r.Sweep()
	if got := len(r.Bans("ch")); got != 0 {
		t.Errorf("%d expired bans survived Sweep", got)
	}
	// Idle past the reset horizon and the whole channel should go.
	clk.advance(r.Policy().idleResetAfter() + time.Minute)
	r.Sweep()
	for _, st := range r.Channels() {
		if st.Name == "ch" {
			t.Error("fully idle, empty channel survived Sweep")
		}
	}
}

func TestSweepKeepsLiveBans(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 3600 })
	banNow(t, r, "ch", "1.1.1.1:80")
	r.Sweep()
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Error("Sweep released a ban that had not expired")
	}
}

func TestUnbanReleasesButKeepsLadder(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) {
		p.BanTTLSec = 60
		p.BanTTLMaxSec = 1800
	})
	banNow(t, r, "ch", "1.1.1.1:80")
	if !r.Unban("ch", "1.1.1.1:80") {
		t.Fatal("Unban reported no ban to clear")
	}
	if r.Banned("ch", "1.1.1.1:80") {
		t.Fatal("still banned after Unban")
	}
	// Re-offending picks up the ladder rather than restarting at the base TTL: a
	// manual release is a second chance, not amnesia.
	b := banNow(t, r, "ch", "1.1.1.1:80")
	if b.TTLSec != 120 {
		t.Errorf("TTL after manual unban then re-offence = %ds, want 120", b.TTLSec)
	}
}

func TestUnbanUnknownPairIsNoOp(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	if r.Unban("nope.com", "1.1.1.1:80") {
		t.Error("Unban reported success for a channel that was never seen")
	}
}

func TestResetChannelClearsEverything(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	banNow(t, r, "ch", "1.1.1.1:80")
	banNow(t, r, "ch", "2.2.2.2:80")
	if !r.ResetChannel("ch") {
		t.Fatal("ResetChannel reported no such channel")
	}
	if got := len(r.Bans("ch")); got != 0 {
		t.Errorf("%d bans survived the reset", got)
	}
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("proxy still banned after channel reset")
	}
}

func TestBanSetMatchesBannedForEveryEntry(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 3600 })
	banNow(t, r, "ch", "1.1.1.1:80")
	banNow(t, r, "ch", "2.2.2.2:80")
	ok(r, "ch", "3.3.3.3:80")

	set := r.BanSet("ch")
	if len(set) != 2 {
		t.Fatalf("BanSet returned %d entries, want 2", len(set))
	}
	// BanSet is the bulk form of Banned; the two must never disagree, since
	// selection trusts BanSet for a whole pick.
	for _, addr := range []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80"} {
		_, inSet := set[addr]
		if inSet != r.Banned("ch", addr) {
			t.Errorf("%s: BanSet says %v, Banned says %v", addr, inSet, r.Banned("ch", addr))
		}
	}
}

func TestTotalsCountsChannelsAndBans(t *testing.T) {
	r, _ := newTestRegistry(t, func(p *Policy) { p.BanTTLSec = 3600 })
	banNow(t, r, "a.com", "1.1.1.1:80")
	banNow(t, r, "b.com", "2.2.2.2:80")
	ok(r, "c.com", "3.3.3.3:80")
	channels, bans := r.Totals()
	if channels != 3 {
		t.Errorf("channels = %d, want 3", channels)
	}
	if bans != 2 {
		t.Errorf("bans = %d, want 2", bans)
	}
}
