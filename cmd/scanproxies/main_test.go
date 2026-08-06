package main

import (
	"strings"
	"testing"

	"unified-proxy-pool/internal/freproxies"
)

// A scan that shrinks the pool is the most surprising outcome this command can
// produce, so it must always be explained. Writing 21 proxies into a raw pool
// that was already over MaxRawProxies dropped the total by 383: Trim evicts the
// lowest-scoring raw entries, and every fresh proxy enters at ScoreInit, so the
// eviction lands on ties.
func TestCapNoteFiresOnNetLoss(t *testing.T) {
	note := capNote(counts{total: 4387, raw: 4385}, counts{total: 4004, raw: 4000}, 21, 20)
	if note == "" {
		t.Fatal("a net-negative write must be explained; scanning 21 proxies should not silently cost 383")
	}
	if !strings.Contains(note, "Trim") {
		t.Errorf("the note must name the mechanism, got: %s", note)
	}
}

// After validating, proxies measured as dead must not be inserted as new raw
// entries. They enter at ScoreInit, occupy raw-cap space, and Trim then evicts
// other sources' proxies to make room — all to store addresses we just proved
// do not work.
//
// Measured round that exposed this: 278 candidates, 42 alive, 236 dead. All 278
// went to AddRaw; 225 were new; Trim evicted 225; the pool netted total+0.
func TestWriteSetExcludesNewlyMeasuredDead(t *testing.T) {
	results := []checked{
		{proxy: freproxies.Proxy{Addr: "1.1.1.1:80", Host: "1.1.1.1", Port: 80}, alive: true, latencyMS: 100},
		{proxy: freproxies.Proxy{Addr: "2.2.2.2:80", Host: "2.2.2.2", Port: 80}, alive: false},
		{proxy: freproxies.Proxy{Addr: "3.3.3.3:80", Host: "3.3.3.3", Port: 80}, alive: true, latencyMS: 200},
		{proxy: freproxies.Proxy{Addr: "4.4.4.4:80", Host: "4.4.4.4", Port: 80}, alive: false},
	}

	got := writeSet(results, false)
	if len(got) != 2 {
		t.Fatalf("expected only the 2 alive proxies to be inserted, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Addr == "2.2.2.2:80" || p.Addr == "4.4.4.4:80" {
			t.Errorf("a proxy measured dead was queued for insertion: %s", p.Addr)
		}
	}
}

// Without validation there is no verdict to act on, so everything is written and
// the panel's validator scores them later.
func TestWriteSetKeepsEverythingWhenSkippingValidation(t *testing.T) {
	results := []checked{
		{proxy: freproxies.Proxy{Addr: "1.1.1.1:80"}},
		{proxy: freproxies.Proxy{Addr: "2.2.2.2:80"}},
	}
	if got := writeSet(results, true); len(got) != 2 {
		t.Errorf("-skip-validate must write all candidates, got %d", len(got))
	}
}

// Eviction cannot be inferred from the raw count alone. Measured run: AddRaw
// reported 254 new and 42 were promoted, yet raw held at exactly 4000 and the
// total rose by 42 — so both the net-loss and oversized-scan checks stayed
// quiet while 212 proxies were silently evicted. The additions and the
// evictions cancelled out in the delta.
func TestCapNoteDetectsEvictionMaskedByPromotions(t *testing.T) {
	before := counts{total: 4004, validated: 4, raw: 4000}
	after := counts{total: 4046, validated: 46, raw: 4000}

	note := capNote(before, after, 257, 254)
	if note == "" {
		t.Fatal("212 proxies left the pool; a flat raw count and a positive total must not hide that")
	}
	if !strings.Contains(note, "212") {
		t.Errorf("the note must state how many were evicted, got: %s", note)
	}
}

// A scan larger than the cap will evict regardless of the starting state.
func TestCapNoteFiresOnOversizedScan(t *testing.T) {
	big := freproxies.MaxRawProxies * 2
	note := capNote(counts{total: 10, raw: 10},
		counts{total: int64(freproxies.MaxRawProxies), raw: int64(freproxies.MaxRawProxies)}, big, big)
	if note == "" {
		t.Fatalf("a scan of %d into a cap of %d must warn", big, freproxies.MaxRawProxies)
	}
}

// The common case — a modest scan into a pool with headroom — must stay quiet,
// or the note becomes noise that gets ignored when it matters.
func TestCapNoteSilentWhenPoolHasHeadroom(t *testing.T) {
	if note := capNote(counts{total: 100, raw: 100}, counts{total: 140, raw: 138}, 50, 40); note != "" {
		t.Errorf("expected no note for a normal write, got: %s", note)
	}
}

// Promoting raw proxies to validated moves them between sets, so raw can fall
// while the total holds. That is not an eviction and must not warn.
func TestCapNoteSilentWhenRawFallsFromPromotion(t *testing.T) {
	before := counts{total: 500, validated: 0, raw: 500}
	after := counts{total: 500, validated: 40, raw: 460}
	// Nothing new was added, so the whole raw drop is promotion.
	if note := capNote(before, after, 500, 0); note != "" {
		t.Errorf("promotion is not eviction; expected no note, got: %s", note)
	}
}
