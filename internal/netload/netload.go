// Package netload measures how busy the panel's own network path is, so
// background work — proxy validation above all — can get out of the way of the
// people actually using the proxies.
//
// The problem it solves: validation runs a fixed 32 concurrent probes against a
// 4000-entry raw pool, back to back, forever. On a soft router that is enough
// to saturate the uplink and the NAT table while the operator is trying to use
// the very proxies the panel is testing. Two signals matter:
//
//   - egress bytes/sec, sampled from the global traffic counter and smoothed
//     over a window so one burst does not throttle the pool for an hour
//   - live client connections, which is the clearest possible "someone is
//     using the network right now" signal
package netload

import (
	"context"
	"math"
	"sync"
	"time"

	"unified-proxy-pool/internal/traffic"
)

// Defaults are deliberately permissive: this throttles background work, it must
// never make the panel feel broken on a normal home link. Raise or lower them
// with the fields on Sampler.
const (
	defaultEgressBytesPerSec = 4 << 20 // 4 MiB/s combined up+down before we consider the link busy
	defaultClientConns       = 16      // live client tunnels above this count as "someone is using it"
	windowSize               = 10      // samples kept; at 1s/tick this is a 10s memory
	tickInterval             = time.Second
)

// Level is a point-in-time reading of how contended the network path is.
type Level struct {
	// EgressBytesPerSec is the busiest combined up+down sample in the window.
	EgressBytesPerSec float64 `json:"egress_bytes_per_sec"`
	// ClientConns is live inbound client connections right now.
	ClientConns int64 `json:"client_conns"`
	// Score is 0 (idle) to 1 (saturated). It is the max of the two signals, so
	// either a saturated link or a busy client load alone is enough to throttle.
	Score float64 `json:"score"`
	// Reason names whichever signal is currently dominant, for the operator log.
	Reason string `json:"reason"`
}

// Busy reports whether background work should yield. A non-empty reason means
// yes, and says why.
func (l Level) Busy() (reason string, ok bool) {
	return l.Reason, l.Reason != ""
}

// Sampler watches the traffic counter and answers "how busy is the network".
// It is safe for concurrent use; Level is a snapshot and needs no lock by the
// caller.
type Sampler struct {
	// snapshot supplies the counter readings. traffic.Default is the normal
	// one; tests inject a fake.
	snapshot func() traffic.Snapshot

	// EgressBytesPerSecBusy is the combined up+down rate above which the link
	// counts as contended.
	EgressBytesPerSecBusy float64
	// ClientConnsBusy is the live client connection count above which we
	// assume someone is actively using the panel's proxies.
	ClientConnsBusy int64

	mu       sync.Mutex
	lastUp   int64
	lastDown int64
	lastAt   time.Time
	window   []float64 // ring of bytes/sec samples, oldest first
}

// New wires the sampler to the given counter reader.
func New(snapshot func() traffic.Snapshot) *Sampler {
	return &Sampler{
		snapshot:              snapshot,
		EgressBytesPerSecBusy: defaultEgressBytesPerSec,
		ClientConnsBusy:       defaultClientConns,
	}
}

// Default is the process-wide sampler, reading the real traffic counter.
var Default = New(func() traffic.Snapshot { return traffic.Default.Snapshot() })

// Start begins sampling in the background until ctx is done. Sampling is
// cheap; the window is bounded and the counter is already lock-protected.
func (s *Sampler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sample()
			}
		}
	}()
}

// sample takes one differential reading. Counter values are cumulative, so the
// rate is the delta since the last sample.
func (s *Sampler) sample() {
	snap := s.snapshot()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	up, down := snap.UpBytes, snap.DownBytes
	if !s.lastAt.IsZero() {
		elapsed := now.Sub(s.lastAt).Seconds()
		if elapsed > 0 {
			// A negative delta means the counter restarted; treat it as no
			// traffic rather than a nonsensical negative rate.
			delta := float64((up - s.lastUp) + (down - s.lastDown))
			if delta < 0 {
				delta = 0
			}
			rate := delta / elapsed
			s.window = append(s.window, rate)
			if len(s.window) > windowSize {
				s.window = s.window[1:]
			}
		}
	}
	s.lastUp, s.lastDown, s.lastAt = up, down, now
}

// Level returns the current contention reading.
func (s *Sampler) Level() Level {
	s.mu.Lock()
	window := append([]float64(nil), s.window...)
	s.mu.Unlock()

	busiest := 0.0
	for _, v := range window {
		if v > busiest {
			// The window max, not the mean: a link that is saturated half the
			// time is a link background work should yield on.
			busiest = v
		}
	}
	snap := s.snapshot()

	lvl := Level{
		EgressBytesPerSec: busiest,
		ClientConns:       snap.ActiveIn,
	}

	score := 0.0
	reason := ""
	if s.EgressBytesPerSecBusy > 0 {
		score = math.Min(1, busiest/s.EgressBytesPerSecBusy)
		if score >= 1 {
			reason = "egress saturated"
		}
	}
	if s.ClientConnsBusy > 0 && snap.ActiveIn >= s.ClientConnsBusy {
		connScore := math.Min(1, float64(snap.ActiveIn)/float64(s.ClientConnsBusy))
		if connScore > score {
			score = connScore
			reason = "clients active"
		} else if reason == "" {
			reason = "clients active"
		}
	}
	lvl.Score = score
	lvl.Reason = reason
	return lvl
}
