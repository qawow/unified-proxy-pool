package freproxies

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"unified-proxy-pool/internal/crawlers"
)

func TestTestProxyURLsPreservesActualCause(t *testing.T) {
	for _, wasValidated := range []bool{false, true} {
		t.Run(fmt.Sprintf("validated=%v", wasValidated), func(t *testing.T) {
			s := newPickService(t)
			ctx := context.Background()
			p := Proxy{Host: "198.51.100.10", Port: 8080, Protocol: "http", Source: "test"}
			if _, err := s.store.AddRaw(ctx, []Proxy{p}); err != nil {
				t.Fatal(err)
			}
			addr := normalizeAddr(p.Host, p.Port)
			if wasValidated {
				if err := s.store.MarkValidated(ctx, addr, 100, true); err != nil {
					t.Fatal(err)
				}
			}
			s.SetProbeFront(func() *ProbeFront {
				return &ProbeFront{Hop: Proxy{Addr: "192.0.2.1:1080", Protocol: "socks5"}}
			}, func(context.Context, []Proxy, string) (net.Conn, error) {
				return nil, context.DeadlineExceeded
			})
			_, err := s.TestProxyURLs(ctx, addr, []string{"https://example.test/"}, time.Second, false)
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errValidationFailed) {
				t.Fatalf("validation lost the probe cause: %v", err)
			}
		})
	}
}

type failingHealthStore struct {
	Store
	listErr, queueErr error
}

func (s *failingHealthStore) ListValidated(ctx context.Context, n int64) ([]Proxy, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.ListValidated(ctx, n)
}
func (s *failingHealthStore) Queues(ctx context.Context) (ValidatorQueues, error) {
	if s.queueErr != nil {
		return ValidatorQueues{}, s.queueErr
	}
	return s.Store.Queues(ctx)
}

func TestHealthReadsBandsFromStore(t *testing.T) {
	factories := map[string]func(*testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemoryStore() },
		"redis":  func(t *testing.T) Store { s, _ := newFakeRedisStore(t); return s },
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			for i, ms := range []int64{0, 100, 600, 2000, 4000} {
				host := fmt.Sprintf("198.51.100.%d", i+1)
				if _, err := store.AddRaw(ctx, []Proxy{{Host: host, Port: 8080, Protocol: "http"}}); err != nil {
					t.Fatal(err)
				}
				if err := store.MarkValidated(ctx, normalizeAddr(host, 8080), ms, true); err != nil {
					t.Fatal(err)
				}
			}
			h := NewService(store, nil, nil, false).Health(ctx)
			if h.Available != 5 || h.Fast != 1 || h.Usable != 1 || h.Slow != 2 || h.UnknownLatency != 1 || h.MedianLatency != 1300 || h.FastestLatency != 100 || h.Status != "degraded" {
				t.Fatalf("unexpected store-derived health: %+v", h)
			}
		})
	}
}

func TestHealthDistinguishesUnavailableFromEmpty(t *testing.T) {
	for _, stage := range []string{"list", "queues"} {
		t.Run(stage, func(t *testing.T) {
			st := &failingHealthStore{Store: NewMemoryStore()}
			if stage == "list" {
				st.listErr = errors.New("store offline")
			} else {
				st.queueErr = errors.New("store offline")
			}
			s := NewService(st, nil, nil, false)
			if h := s.Health(t.Context()); h.Status != "unknown" {
				t.Fatalf("backend error reported as %+v", h)
			}
		})
	}
	st := &listValidatedStore{Store: NewMemoryStore()}
	s := NewService(st, crawlers.NewRegistry(nil), nil, false)
	for i := 0; i < 5; i++ {
		if h := s.Health(t.Context()); h.Status != "empty" {
			t.Fatalf("empty store reported as %+v", h)
		}
	}
	if n := st.lists.Load(); n != 1 {
		t.Fatalf("empty health made %d store reads", n)
	}
	ov, err := s.Overview(t.Context())
	if err != nil || ov.PoolHealth == nil || ov.PoolHealth.Status != "empty" {
		t.Fatalf("empty overview lost health: %+v, %v", ov.PoolHealth, err)
	}
}

func TestCancelledHealthRefreshDoesNotPolluteCache(t *testing.T) {
	st := &failingHealthStore{Store: NewMemoryStore()}
	s := NewService(st, nil, nil, false)
	s.Health(t.Context())
	old := time.Now().Add(-healthTTL - time.Second)
	s.healthAt = old
	st.listErr = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h := s.Health(ctx); h.Status != "unknown" {
		t.Fatalf("cancelled refresh: %+v", h)
	}
	if !s.healthAt.Equal(old) || s.healthCache.Status != "empty" {
		t.Fatal("cancelled refresh replaced cached snapshot")
	}
}
