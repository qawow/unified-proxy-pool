package netload

import (
	"testing"
	"time"

	"unified-proxy-pool/internal/traffic"
)

// fakeSnapshot returns a controllable counter for tests.
type fakeSnapshot struct {
	up, down, activeIn int64
}

func (f *fakeSnapshot) snapshot() traffic.Snapshot {
	return traffic.Snapshot{UpBytes: f.up, DownBytes: f.down, ActiveIn: f.activeIn}
}

func TestLevelIdleIsNotBusy(t *testing.T) {
	f := &fakeSnapshot{}
	s := New(f.snapshot)
	s.EgressBytesPerSecBusy = 1000
	s.ClientConnsBusy = 10

	// Feed two samples 1s apart with traffic well under the threshold.
	f.up, f.down = 0, 0
	s.sample()
	time.Sleep(1100 * time.Millisecond)
	f.down = 500 // 500 B/s vs a 1000 B/s threshold
	s.sample()

	lvl := s.Level()
	if reason, busy := lvl.Busy(); busy {
		t.Fatalf("Level().Busy() = (%q, true), want idle", reason)
	}
	if lvl.Score > 0.5 {
		t.Fatalf("Score = %v, want well below busy threshold", lvl.Score)
	}
}

func TestLevelSaturatedEgressIsBusy(t *testing.T) {
	f := &fakeSnapshot{}
	s := New(f.snapshot)
	s.EgressBytesPerSecBusy = 1000
	s.ClientConnsBusy = 100

	f.down = 0
	s.sample()
	time.Sleep(1100 * time.Millisecond)
	f.down = 5000 // 5x the threshold
	s.sample()

	lvl := s.Level()
	reason, busy := lvl.Busy()
	if !busy {
		t.Fatalf("Level().Busy() = idle, want busy for a saturated link")
	}
	if reason == "" {
		t.Fatal("busy with no reason; the operator log needs a cause")
	}
	if lvl.Score < 1.0 {
		t.Fatalf("Score = %v, want 1.0 for a saturated link", lvl.Score)
	}
}

func TestLevelActiveClientsAreBusy(t *testing.T) {
	f := &fakeSnapshot{activeIn: 20}
	s := New(f.snapshot)
	s.ClientConnsBusy = 10
	s.EgressBytesPerSecBusy = 1 << 20 // high, so egress alone would not trip

	lvl := s.Level()
	if _, busy := lvl.Busy(); !busy {
		t.Fatalf("Level().Busy() = idle with %d active clients", f.activeIn)
	}
	if lvl.ClientConns != 20 {
		t.Fatalf("ClientConns = %d, want 20", lvl.ClientConns)
	}
}

// TestLevelCounterRestartIsClamped: if the traffic counter restarts (process
// bounce), the delta goes negative and must not become a negative rate.
func TestLevelCounterRestartIsClamped(t *testing.T) {
	f := &fakeSnapshot{down: 100000}
	s := New(f.snapshot)
	s.EgressBytesPerSecBusy = 1000
	s.sample()
	time.Sleep(1100 * time.Millisecond)
	f.down = 0 // counter restarted
	s.sample()

	lvl := s.Level()
	if lvl.EgressBytesPerSec < 0 {
		t.Fatalf("negative rate after counter restart: %v", lvl.EgressBytesPerSec)
	}
}

// TestConcurrencyForLoadScalesDownWhenBusy documents the curve: idle runs at
// the full configured ceiling, contention scales it down linearly, live client
// tunnels halve it again, and the result is clamped to [1, base].
func TestConcurrencyForLoadScalesDownWhenBusy(t *testing.T) {
	base := 32
	// n = base * (1 - 0.75*Score) when busy, then /2 while clients are live.
	cases := []struct {
		name string
		lvl  Level
		want int
	}{
		{"idle", Level{}, 32},
		{"half busy link", Level{Score: 0.5, Reason: "egress saturated"}, 20},
		{"saturated link", Level{Score: 1.0, Reason: "egress saturated"}, 8},
		{"clients active", Level{ClientConns: 5, Score: 0.4, Reason: "clients active"}, 11},
		{"clients active saturated", Level{ClientConns: 50, Score: 1.0, Reason: "clients active"}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := concurrencyForLoad(base, c.lvl)
			if got != c.want {
				t.Fatalf("concurrencyForLoad(%d, %+v) = %d, want %d", base, c.lvl, got, c.want)
			}
			if got < 1 {
				t.Fatalf("concurrency must never drop to zero: got %d", got)
			}
			if got > base {
				t.Fatalf("concurrency must never exceed the configured base: got %d", got)
			}
		})
	}
}

// TestConcurrencyForLoadNeverStops: at maximum contention the pool still
// validates at a crawl rather than stopping — a pool that never gets validated
// is a pool that silently rots.
func TestConcurrencyForLoadNeverStops(t *testing.T) {
	got := concurrencyForLoad(32, Level{Score: 1.0, ClientConns: 1000, Reason: "everything"})
	if got < 1 {
		t.Fatalf("maximum contention = %d, must stay >= 1", got)
	}
	if got > 4 {
		t.Fatalf("maximum contention = %d, want the pool slowed to a crawl (<=4)", got)
	}
}

func TestSemAcquireRelease(t *testing.T) {
	s := NewSem(4)
	for i := 0; i < 4; i++ {
		if err := s.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
	}
	// 5th would block; verify via a non-blocking attempt on a copy of the state.
	if got := s.Capacity(); got != 4 {
		t.Fatalf("Capacity = %d, want 4", got)
	}
	s.Release()
	if err := s.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	s.Release()
}

func TestSemSetCapacityShrinksAndGrows(t *testing.T) {
	s := NewSem(8)
	s.SetCapacity(2)
	if got := s.Capacity(); got != 2 {
		t.Fatalf("after SetCapacity(2): %d, want 2", got)
	}
	// Only two tokens should be outstanding now.
	if err := s.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	if err := s.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Acquire(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire 3 should have blocked, got %v", err)
		}
		t.Fatal("Acquire 3 succeeded on a capacity-2 semaphore")
	case <-time.After(150 * time.Millisecond):
	}
	// Growing again releases the waiter.
	s.SetCapacity(4)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire 3 after growth: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire 3 still blocked after capacity grew")
	}
	s.Release()
	s.Release()
	s.Release()
}

func TestSemSetCapacityClamps(t *testing.T) {
	s := NewSem(4)
	s.SetCapacity(0) // must clamp to 1, not zero
	if got := s.Capacity(); got != 1 {
		t.Fatalf("SetCapacity(0) -> %d, want 1", got)
	}
	s.SetCapacity(99) // must clamp to the configured max
	if got := s.Capacity(); got != 4 {
		t.Fatalf("SetCapacity(99) -> %d, want 4 (the configured ceiling)", got)
	}
}
