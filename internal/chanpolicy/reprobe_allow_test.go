package chanpolicy

import (
	"testing"
	"time"
)

func TestBanStaysAfterTTLUntilSuccess(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.BanTTLSec = 60
		p.ReprobeOnExpiry = true
	})
	banNow(t, r, "ch", "1.1.1.1:80")
	clk.advance(90 * time.Second)
	if !r.Banned("ch", "1.1.1.1:80") {
		t.Fatal("ban released at TTL; a just-403'd IP must stay out until something succeeds")
	}
	ok(r, "ch", "1.1.1.1:80")
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("still banned after a success that should have cleared the reprobe")
	}
}

func TestReprobeOffReleasesAtTTL(t *testing.T) {
	r, clk := newTestRegistry(t, func(p *Policy) {
		p.BanTTLSec = 60
		p.ReprobeOnExpiry = false
	})
	banNow(t, r, "ch", "1.1.1.1:80")
	clk.advance(90 * time.Second)
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("reprobe is off, TTL elapsed, but still banned")
	}
}

func TestAllowlistBlocksAutomaticBan(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	r.Allow("taobao.com", "1.1.1.1:80", "bought residential")
	if b := r.Record(Outcome{Channel: "taobao.com", Addr: "1.1.1.1:80", Status: 403}); b != nil {
		t.Fatalf("allowlisted addr was banned: %+v", b)
	}
	if r.Banned("taobao.com", "1.1.1.1:80") {
		t.Error("allowlisted addr shows as banned")
	}
}

func TestGlobalAllowProtectsEveryChannel(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	r.Allow("", "1.1.1.1:80", "never")
	if b := r.Record(Outcome{Channel: "amazon.com", Addr: "1.1.1.1:80", Status: 429}); b != nil {
		t.Fatal("global allow did not protect amazon.com")
	}
}

func TestAllowReleasesExistingBan(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	banNow(t, r, "ch", "1.1.1.1:80")
	r.Allow("ch", "1.1.1.1:80", "ops")
	if r.Banned("ch", "1.1.1.1:80") {
		t.Error("Allow left the existing ban in place")
	}
}

func TestDenyRemovesProtection(t *testing.T) {
	r, _ := newTestRegistry(t, nil)
	r.Allow("ch", "1.1.1.1:80", "tmp")
	if !r.Deny("ch", "1.1.1.1:80") {
		t.Fatal("Deny reported nothing to remove")
	}
	if b := r.Record(Outcome{Channel: "ch", Addr: "1.1.1.1:80", Status: 403}); b == nil {
		t.Error("after Deny, a 403 did not ban")
	}
}
