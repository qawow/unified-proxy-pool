package sticky

import (
	"sync"
	"time"
)

type Store struct {
	mu  sync.RWMutex
	ttl time.Duration
	// clientIP -> upstream addr
	m map[string]entry
}

type entry struct {
	addr string
	exp  time.Time
}

func New(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Store{ttl: ttl, m: map[string]entry{}}
}

func (s *Store) SetTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	s.ttl = d
	s.mu.Unlock()
}

func (s *Store) Get(clientIP string) (string, bool) {
	if s == nil || clientIP == "" {
		return "", false
	}
	now := time.Now()
	s.mu.RLock()
	e, ok := s.m[clientIP]
	s.mu.RUnlock()
	if !ok || now.After(e.exp) {
		return "", false
	}
	return e.addr, true
}

func (s *Store) Put(clientIP, addr string) {
	if s == nil || clientIP == "" || addr == "" {
		return
	}
	s.mu.Lock()
	s.m[clientIP] = entry{addr: addr, exp: time.Now().Add(s.ttl)}
	// opportunistic cleanup
	if len(s.m) > 5000 {
		now := time.Now()
		for k, v := range s.m {
			if now.After(v.exp) {
				delete(s.m, k)
			}
		}
	}
	s.mu.Unlock()
}
