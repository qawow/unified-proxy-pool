package netload

import (
	"context"
	"sync"
	"time"
)

// Sem is a counting semaphore whose capacity can change while work is in
// flight. The validator used to take its concurrency from settings as a fixed
// channel size; making that adaptive is the whole point, and Go channels cannot
// be resized, so this keeps a buffer sized at the configured maximum and loans
// out a variable number of tokens.
//
// The capacity is a soft target: a momentary overshoot of one or two in-flight
// probes is harmless, whereas making every probe block on a lock would cost
// more than it saves.
type Sem struct {
	max int

	mu     sync.Mutex
	tokens chan struct{}
	// current is the number of tokens that should be outstanding. It is tracked
	// separately from len(tokens) because tokens sitting in the channel are
	// "available" rather than "in use".
	current int
}

// NewSem creates a semaphore that will hand out up to max concurrent tokens.
// The capacity may later be lowered below max, never raised above it.
func NewSem(max int) *Sem {
	if max < 1 {
		max = 1
	}
	tokens := make(chan struct{}, max)
	for i := 0; i < max; i++ {
		tokens <- struct{}{}
	}
	return &Sem{max: max, tokens: tokens, current: max}
}

// Acquire waits for a token or for ctx to be cancelled. A cancelled context
// returns without taking a token, so a probe that never runs does not get
// blamed on a proxy later.
func (s *Sem) Acquire(ctx context.Context) error {
	select {
	case <-s.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a token. If the capacity has since been lowered below the
// number of outstanding tokens, the extra token is dropped rather than pushed
// back — that is how the pool actually shrinks.
func (s *Sem) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.tokens <- struct{}{}:
	default:
		// Channel is full: capacity shrank while this token was out. Discard
		// it so future Acquire calls see the new, lower limit.
		if s.current > 0 {
			s.current--
		}
	}
}

// SetCapacity adjusts how many probes may run at once. It takes or tops up
// tokens so len(tokens) tracks the new target. Values are clamped to
// [1, max].
func (s *Sem) SetCapacity(n int) {
	if n < 1 {
		n = 1
	}
	if n > s.max {
		n = s.max
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == s.current {
		return
	}
	// Pull surplus tokens out of circulation, or add missing ones back.
	for len(s.tokens) > n {
		select {
		case <-s.tokens:
		default:
		}
	}
	for len(s.tokens) < n {
		select {
		case s.tokens <- struct{}{}:
		default:
		}
	}
	s.current = n
}

// Capacity reports the current target.
func (s *Sem) Capacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Watch drives a semaphore's capacity from the measured network load until the
// returned stop function is called. It is how the validator becomes adaptive:
//
//	base   concurrency from settings, the ceiling
//	idle   no clients and a quiet link -> full base concurrency
//	busy   clients are using the proxies, or the link is saturated
//
// When busy the capacity is scaled down by Score and halved again while client
// tunnels are live, flooring at 1 so validation never fully stops — a pool
// that never gets validated is a pool that silently rots. The clamp to base
// also means a misconfigured ceiling cannot lift concurrency past what the
// operator set.
func (s *Sampler) Watch(ctx context.Context, sem *Sem, base int) (stop func()) {
	if s == nil || sem == nil {
		return func() {}
	}
	if base < 1 {
		base = 1
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				sem.SetCapacity(concurrencyForLoad(base, s.Level()))
			}
		}
	}()
	return func() { close(done) }
}

// concurrencyForLoad maps a load reading onto a concurrency target. It is
// separate from Watch so it can be unit-tested without a running ticker.
func concurrencyForLoad(base int, lvl Level) int {
	n := base
	if lvl.Reason != "" {
		// Scale down in proportion to how contended the path is.
		n = int(float64(base) * (1.0 - 0.75*lvl.Score))
	}
	if lvl.ClientConns > 0 {
		// Someone is using the panel right now. Client traffic is what the
		// uplink is for; validation is background work and can wait.
		n = n / 2
	}
	if n < 1 {
		n = 1
	}
	if n > base {
		n = base
	}
	return n
}
