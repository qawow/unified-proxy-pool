package freproxies

import (
	"math/rand"
	"sync"
	"testing"
)

// TestPickRNGConcurrentUse: the lock must cover the *use* of the source, not
// just its construction. Concurrent draws each got a *rand.Rand over ONE shared
// source while only rand.New(src) was locked, so the concurrent Intn/Float64
// calls raced and -race fired in the real selection path. withRand now holds
// the lock for the whole draw; this test fails (data race) if it is ever
// widened back out.
func TestPickRNGConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				withRand(func(r *rand.Rand) {
					_ = r.Intn(1000)
					_ = r.Float64()
				})
			}
		}()
	}
	wg.Wait()
}
