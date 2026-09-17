package freproxies

import (
	"sync"
	"testing"
	"time"
)

// TestSetPickDefaultsConcurrentRead: SetPickDefaults is hot-applied from the
// settings handler while Pick reads the default strategy on every proxy
// selection. Unlocked, that is a live data race under concurrent client load.
// This pins both sides behind the RWMutex; a widening back to plain field
// access fails under -race.
func TestSetPickDefaultsConcurrentRead(t *testing.T) {
	store := NewMemoryStore()
	s := NewService(store, nil, nil, false)

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Writer: the settings handler saving a new strategy.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			s.SetPickDefaults("weighted", time.Second)
			s.SetPickDefaults("random", time.Second)
		}
		close(done)
	}()

	// Readers: the selection path reading the default, concurrently.
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				store := NewMemoryStore()
				svc := NewService(store, nil, nil, false)
				_, _ = svc.Pick(t.Context(), PickOptions{N: 1})
			}
		}()
	}
	wg.Wait()
}
