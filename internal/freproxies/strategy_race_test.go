package freproxies

import (
	"sync"
	"testing"
	"time"
)

// Readers and the settings writer must use the same Service to exercise the
// shared defaultStrategy field under the race detector.
func TestSetPickDefaultsConcurrentRead(t *testing.T) {
	s := newPickService(t, proxyAt("198.51.100.19", 8080, 90, 100))
	s.SetPickDefaults("weighted", time.Second)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 400; i++ {
			s.SetPickDefaults("weighted", time.Second)
			s.SetPickDefaults("random", time.Second)
		}
	}()
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				result, err := s.Pick(t.Context(), PickOptions{N: 1, NoCooldown: true})
				if err != nil || len(result.Items) != 1 {
					t.Errorf("concurrent Pick: items=%d err=%v", len(result.Items), err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}
