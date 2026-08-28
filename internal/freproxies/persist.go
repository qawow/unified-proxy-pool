package freproxies

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"unified-proxy-pool/internal/db"
)

// PersistMemoryStore loads a previous SQLite snapshot into a memory store and
// writes back on change (debounced) plus Close. Redis-backed stores are returned unchanged.
func PersistMemoryStore(store Store, d *db.Store) Store {
	ms, ok := store.(*memoryStore)
	if !ok || d == nil {
		return store
	}
	ms.persist = d
	if err := ms.loadSnapshot(context.Background()); err != nil {
		log.Printf("free-proxy snapshot load: %v", err)
	} else {
		log.Printf("free-proxy snapshot loaded from sqlite (%s)", ms.Backend())
	}
	ms.stopPersist = make(chan struct{})
	go ms.persistLoop()
	return ms
}

func (s *memoryStore) markDirty() {
	if s.persist == nil {
		return
	}
	s.dirty.Store(true)
}

func (s *memoryStore) persistLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopPersist:
			s.flushSnapshot()
			return
		case <-t.C:
			s.flushSnapshot()
		}
	}
}

func (s *memoryStore) flushSnapshot() {
	if s.persist == nil || !s.dirty.Swap(false) {
		return
	}
	s.mu.RLock()
	rows := make([]db.FreeProxyRow, 0, len(s.proxies))
	for addr, p := range s.proxies {
		body, err := json.Marshal(p)
		if err != nil {
			continue
		}
		_, raw := s.raw[addr]
		_, scored := s.scored[addr]
		_, retrying := s.retry[addr]
		rows = append(rows, db.FreeProxyRow{Addr: addr, JSON: string(body), InRaw: raw, InScored: scored, InRetry: retrying})
	}
	toggles := map[string]bool{}
	for name := range s.enabled {
		toggles[name] = true
	}
	for name := range s.disabled {
		toggles[name] = false
	}
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.persist.ReplaceFreeProxySnapshot(ctx, rows, toggles); err != nil {
		log.Printf("free-proxy snapshot save: %v", err)
		s.dirty.Store(true)
	}
}

func (s *memoryStore) loadSnapshot(ctx context.Context) error {
	rows, toggles, err := s.persist.LoadFreeProxySnapshot(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		var p Proxy
		if err := json.Unmarshal([]byte(row.JSON), &p); err != nil {
			continue
		}
		if p.Addr == "" {
			p.Addr = row.Addr
		}
		s.proxies[row.Addr] = p
		if row.InRaw {
			s.raw[row.Addr] = struct{}{}
		}
		if row.InScored {
			s.scored[row.Addr] = struct{}{}
		}
		if row.InRetry || (!row.InRaw && !row.InScored && p.FailCount > 0 && !p.Validated) {
			s.retry[row.Addr] = time.Now()
		}
	}
	for name, on := range toggles {
		if on {
			s.enabled[name] = struct{}{}
			delete(s.disabled, name)
		} else {
			s.disabled[name] = struct{}{}
			delete(s.enabled, name)
		}
	}
	return nil
}
