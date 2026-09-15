package freproxies

import (
	"math"
	"testing"
)

// TestQualityScoreIsContinuous: the whole point of this function is that a slow
// proxy and a fast proxy no longer score identically. A flat ScoreMax made
// keyScored useless for ranking and forced RandomN into random sampling.
func TestQualityScoreIsContinuous(t *testing.T) {
	cases := []struct {
		name       string
		latencyMS  int64
		wantNotMax bool
	}{
		{"instant", 0, false},
		{"fast", 100, true},
		{"medium", 1000, true},
		{"slow", 3000, true},
	}
	fast := qualityScore(100)
	slow := qualityScore(3000)
	for _, c := range cases {
		got := qualityScore(c.latencyMS)
		t.Run(c.name, func(t *testing.T) {
			if c.wantNotMax && got == ScoreMax {
				t.Fatalf("qualityScore(%dms) = %v, want below ScoreMax", c.latencyMS, got)
			}
			if got < qualityFloor || got > ScoreMax {
				t.Fatalf("qualityScore(%dms) = %v, out of [floor, max]", c.latencyMS, got)
			}
		})
	}
	// The ordering is what matters: fast must outrank slow.
	if fast <= slow {
		t.Fatalf("fast proxy scores %v, slow scores %v; fast must outrank slow", fast, slow)
	}
	if math.Abs(float64(fast-98)) > 1 {
		t.Fatalf("qualityScore(100ms) = %v, want ~98", fast)
	}
}

func TestQualityScoreFloorsSlowProxies(t *testing.T) {
	// A 10-second proxy is still reachable, not dead.
	got := qualityScore(10000)
	if got != qualityFloor {
		t.Fatalf("qualityScore(10000ms) = %v, want floor %v", got, qualityFloor)
	}
}

func TestSmoothLatencyDampsJitter(t *testing.T) {
	// First reading for an address is taken as-is.
	if got := smoothLatency(0, 500); got != 500 {
		t.Fatalf("smoothLatency(0, 500) = %d, want 500", got)
	}
	// A single bad sample must not dominate a long history of good ones.
	got := smoothLatency(100, 5000)
	if got > 1600 {
		t.Fatalf("smoothLatency(100, 5000) = %d, want the good history to dominate", got)
	}
	if got <= 100 {
		t.Fatalf("smoothLatency(100, 5000) = %d, want the slow sample to move it", got)
	}
	// And recovery from a slow spell should be gradual, not instant.
	rec := smoothLatency(5000, 100)
	if rec < 3000 {
		t.Fatalf("smoothLatency(5000, 100) = %d, want recovery to be gradual", rec)
	}
}

func TestBlendScoreEasesTowardQuality(t *testing.T) {
	// A live proxy at 95 that turns in a slow reading eases down, not drops.
	got := blendScore(95, 40)
	if got < 70 || got > 85 {
		t.Fatalf("blendScore(95, 40) = %v, want ~79", got)
	}
	// Improvement is also gradual, so one good probe cannot crown a slow proxy.
	rec := blendScore(40, 98)
	if rec > 60 {
		t.Fatalf("blendScore(40, 98) = %v, want gradual improvement", rec)
	}
}
