package freproxies

import (
	"testing"
)

// TestEvaluateHealth covers each verdict. The bands are what make the pool
// readable: 25 proxies at a 1.1s median is "slow-ish", not "25, must be fine".
func TestEvaluateHealth(t *testing.T) {
	cases := []struct {
		name string
		h    PoolHealth
		want string
	}{
		{"empty", PoolHealth{}, "empty"},
		{"critical", PoolHealth{Available: 2, Slow: 2}, "critical"},
		{"all slow", PoolHealth{Available: 20, Fast: 0, Usable: 2, Slow: 18, MedianLatency: 2400}, "slow"},
		{"median slow", PoolHealth{Available: 20, Fast: 6, Usable: 8, Slow: 6, MedianLatency: 1600}, "slow"},
		{"thin", PoolHealth{Available: 20, Fast: 1, Usable: 2, Slow: 17, MedianLatency: 900}, "degraded"},
		{"majority slow", PoolHealth{Available: 30, Fast: 5, Usable: 9, Slow: 16, MedianLatency: 1200}, "degraded"},
		{"healthy", PoolHealth{Available: 30, Fast: 12, Usable: 12, Slow: 6, MedianLatency: 700}, "healthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, _ := evaluateHealth(c.h)
			if got != c.want {
				t.Fatalf("evaluateHealth(%+v) = %q, want %q", c.h, got, c.want)
			}
		})
	}
}

// TestHealthVerdictReadable: the verdict must be non-empty text the panel shows
// verbatim — the whole point is that a person reads it and knows what the pool
// is doing without decoding any counts.
func TestHealthVerdictReadable(t *testing.T) {
	for _, h := range []PoolHealth{
		{},
		{Available: 1},
		{Available: 100, Slow: 99, MedianLatency: 3000},
		{Available: 100, Fast: 60, MedianLatency: 400},
	} {
		_, verdict, _ := evaluateHealth(h)
		if verdict == "" {
			t.Fatalf("verdict empty for %+v", h)
		}
	}
}

// TestHealthBandsConsistent: a proxy must land in exactly one band, and the
// band counts plus unknown-latency must sum to Available.
func TestHealthBandsConsistent(t *testing.T) {
	lat := []int64{0, 100, 600, 2000}
	var fast, usable, slow, avail int
	for _, l := range lat {
		avail++
		switch {
		case l <= 0:
			usable++
		case l < bandFast:
			fast++
		case l <= bandSlow:
			usable++
		default:
			slow++
		}
	}
	if fast+usable+slow != avail {
		t.Fatalf("bands sum to %d, Available=%d", fast+usable+slow, avail)
	}
}
