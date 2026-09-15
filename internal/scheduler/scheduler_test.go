package scheduler

import (
	"testing"

	"unified-proxy-pool/internal/netload"
	"unified-proxy-pool/internal/traffic"
)

// A nil sampler is the "feature off" path and must still hand back a usable
// batch size rather than zero.
func TestValidateBatchSizeWithoutNetLoad(t *testing.T) {
	if got := validateBatchSize(nil); got <= 0 {
		t.Fatalf("validateBatchSize(nil) = %d, want a positive batch size", got)
	}
}

// A busy network narrows the batch so it finishes and yields the link, an idle
// one runs wide to drain the raw queue inside one pass.
func TestValidateBatchSizeScalesWithLoad(t *testing.T) {
	s := netload.New(func() traffic.Snapshot { return traffic.Snapshot{ActiveIn: 5} })

	idle := validateBatchSize(s)
	if idle <= 0 {
		t.Fatalf("idle batch size = %d, want positive", idle)
	}

	// Force a busy reading by setting thresholds at zero.
	s.EgressBytesPerSecBusy = 1
	s.ClientConnsBusy = 1
	if _, busy := s.Level().Busy(); !busy {
		t.Fatal("sampler with zero thresholds should report busy")
	}
	busy := validateBatchSize(s)
	if busy <= 0 {
		t.Fatalf("busy batch size = %d, want positive", busy)
	}
	if busy >= idle {
		t.Fatalf("busy batch %d should be narrower than idle %d", busy, idle)
	}
}
