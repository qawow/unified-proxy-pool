package sticky

import (
	"sync"
	"time"
)

type Store struct {
	mu  sync.RWMutex
	ttl time.Duration
	// clientIP -> last successful upstream
	m map[string]entry
}

type entry struct {
	addr     string
	protocol string
	exp      time.Time
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
	addr, _, ok := s.GetProxy(clientIP)
	return addr, ok
}

// GetProxy returns the remembered upstream and the protocol it was dialed with.
// Protocol matters: reconstructing every sticky hit as HTTP CONNECT breaks a
// remembered SOCKS5 proxy.
func (s *Store) GetProxy(clientIP string) (addr, protocol string, ok bool) {
	if s == nil || clientIP == "" {
		return "", "", false
	}
	now := time.Now()
	s.mu.RLock()
	e, found := s.m[clientIP]
	s.mu.RUnlock()
	if !found || now.After(e.exp) {
		return "", "", false
	}
	return e.addr, e.protocol, true
}

func (s *Store) Put(clientIP, addr string) {
	s.PutProxy(clientIP, addr, "")
}

func (s *Store) PutProxy(clientIP, addr, protocol string) {
	if s == nil || clientIP == "" || addr == "" {
		return
	}
	s.mu.Lock()
	s.m[clientIP] = entry{addr: addr, protocol: protocol, exp: time.Now().Add(s.ttl)}
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
