package freproxies

import (
	"context"
	"testing"
)

// TestQualitySnapshotBuckets: the distribution's shape is the health read, so
// the bucketing itself has to be right — a fast proxy in the top bucket, a
// slow one near the floor, and nothing vanishing or double-counted.
func TestQualitySnapshotBuckets(t *testing.T) {
	s := NewMemoryStore().(*memoryStore)
	ctx := context.Background()

	fast := Proxy{Addr: "1.1.1.1:8080", Protocol: "http", Validated: true, Score: 96, LatencyMS: 200}
	slow := Proxy{Addr: "2.2.2.2:8080", Protocol: "http", Validated: true, Score: 30, LatencyMS: 3500}
	s.proxies[fast.Addr] = fast
	s.proxies[slow.Addr] = slow
	s.scored[fast.Addr] = struct{}{}
	s.scored[slow.Addr] = struct{}{}

	snap, err := s.QualitySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Total != 2 {
		t.Fatalf("Total = %d, want 2", snap.Total)
	}
	if snap.AvgScore != 63 {
		t.Fatalf("AvgScore = %v, want 63", snap.AvgScore)
	}
	// Every proxy must land in exactly one bucket.
	var counted int64
	for _, b := range snap.Buckets {
		counted += b.Count
	}
	if counted != 2 {
		t.Fatalf("bucket counts sum to %d, want 2", counted)
	}
	// And the right buckets: 96 in 81-100, 30 in 21-40.
	var high, low int64
	for _, b := range snap.Buckets {
		switch b.Label {
		case "81-100":
			high = b.Count
		case "21-40":
			low = b.Count
		}
	}
	if high != 1 || low != 1 {
		t.Fatalf("high=%d low=%d, want 1 and 1", high, low)
	}
}

func TestQualitySnapshotEmpty(t *testing.T) {
	s := NewMemoryStore().(*memoryStore)
	snap, err := s.QualitySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Total != 0 {
		t.Fatalf("empty pool Total = %d, want 0", snap.Total)
	}
	if snap.AvgScore != 0 {
		t.Fatalf("empty pool AvgScore = %v, want 0", snap.AvgScore)
	}
	// Buckets should still be present so the panel can render an empty chart.
	if len(snap.Buckets) != len(qualityBucketEdges) {
		t.Fatalf("empty pool has %d buckets, want %d", len(snap.Buckets), len(qualityBucketEdges))
	}
}

func TestBucketForScoreEdgeCases(t *testing.T) {
	if got := bucketForScore(100); got != [2]float64{81, 100} {
		t.Fatalf("bucketForScore(100) = %v, want 81-100", got)
	}
	if got := bucketForScore(0); got != [2]float64{0, 21} {
		t.Fatalf("bucketForScore(0) = %v, want the 0-21 band", got)
	}
	// Out-of-range scores clamp rather than fall through the cracks.
	if got := bucketForScore(-5); got != [2]float64{0, 21} {
		t.Fatalf("bucketForScore(-5) = %v, want the 0-21 band", got)
	}
	if got := bucketForScore(150); got != [2]float64{81, 100} {
		t.Fatalf("bucketForScore(150) = %v, want 81-100", got)
	}
}

// TestBucketForScoreGaps: scores are floats (blendScore is an EMA, so 20.5 is a
// routine value), and the old closed ranges {0,20},{21,40},… left every value
// in the open gaps matching no band and falling through to the TOP bucket. The
// panel then counted the worst proxies as the best — the quality read inverted
// exactly where it mattered. Bands are now contiguous: a fractional score like
// 20.5 belongs with the 0-20 band (it is twenty-and-a-half, not twenty-one),
// never with 81-100.
func TestBucketForScoreGaps(t *testing.T) {
	cases := []struct {
		score float64
		want  [2]float64
		label string
	}{
		{20.5, [2]float64{0, 21}, "0-20"},
		{40.7, [2]float64{21, 41}, "21-40"},
		{60.5, [2]float64{41, 61}, "41-60"},
		{80.5, [2]float64{61, 81}, "61-80"},
		{20.999, [2]float64{0, 21}, "0-20"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got := bucketForScore(c.score)
			if got != c.want {
				t.Fatalf("bucketForScore(%g) = %v, want %v", c.score, got, c.want)
			}
			if lbl := bucketLabel(got); lbl != c.label {
				t.Fatalf("bucketLabel(%v) = %q, want %q", got, lbl, c.label)
			}
		})
	}
}
