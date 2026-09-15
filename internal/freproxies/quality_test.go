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
	if got := bucketForScore(0); got != [2]float64{0, 20} {
		t.Fatalf("bucketForScore(0) = %v, want 0-20", got)
	}
	// Out-of-range scores clamp rather than fall through the cracks.
	if got := bucketForScore(-5); got != [2]float64{0, 20} {
		t.Fatalf("bucketForScore(-5) = %v, want 0-20", got)
	}
	if got := bucketForScore(150); got != [2]float64{81, 100} {
		t.Fatalf("bucketForScore(150) = %v, want 81-100", got)
	}
}
