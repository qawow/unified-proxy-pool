package chanpolicy

import (
	"fmt"
	"sync"
	"testing"
)

// Record runs on every proxied request while Banned runs on every selection, so
// the two are permanently concurrent in production. Run under -race.
func TestConcurrentRecordAndQuery(t *testing.T) {
	r := New(Options{Policy: Defaults()})
	const workers = 8
	const iterations = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				channel := fmt.Sprintf("site%d.com", i%5)
				addr := fmt.Sprintf("10.0.%d.%d:80", w, i%7)
				r.Record(Outcome{Channel: channel, Addr: addr, OK: i%3 == 0, Err: "dial_failed"})
				r.Banned(channel, addr)
				r.BanSet(channel)
			}
		}(w)
	}
	// Admin and housekeeping paths take the write lock; they must interleave
	// safely with the hot path.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				r.Channels()
				r.Bans("site1.com")
				r.Totals()
				r.Sweep()
				r.Unban("site2.com", "10.0.0.1:80")
			}
		}()
	}
	// Hot-applying a policy change reaches into every counter.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			p := Defaults()
			if i%2 == 0 {
				p.WindowSec = 120
			}
			r.SetPolicy(p)
		}
	}()
	wg.Wait()
}
