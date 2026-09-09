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
	ms.persistDone = make(chan struct{})
	go ms.persistLoop()
	return ms
}

func (s *memoryStore) markDirty() {
	if s.persist == nil {
		return
	}
	s.dirty.Store(true)
}

// persistInterval debounces snapshot writes. The snapshot is a full
// DELETE+INSERT of every proxy, and validation keeps the store permanently
// dirty, so a 3s tick rewrote thousands of rows continuously. Close() still
// flushes immediately, so a graceful shutdown loses nothing.
const persistInterval = 20 * time.Second

func (s *memoryStore) persistLoop() {
	defer close(s.persistDone)
	t := time.NewTicker(persistInterval)
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
	groups := make([]ProxyGroup, 0, len(s.groups))
	for _, g := range s.groups {
		groups = append(groups, g)
	}
	yields := make(map[string][]SourceYieldRecord, len(s.yields))
	for k, v := range s.yields {
		yields[k] = v
	}
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.persist.ReplaceFreeProxySnapshot(ctx, rows, toggles); err != nil {
		log.Printf("free-proxy snapshot save: %v", err)
		s.dirty.Store(true)
	}
	// Groups and yield history never reached the snapshot, so custom groups and
	// source-tuning history were lost on every restart without Redis.
	if body, err := json.Marshal(groups); err == nil {
		if err := s.persist.SaveKV(ctx, kvGroups, string(body)); err != nil {
			log.Printf("free-proxy groups save: %v", err)
		}
	}
	if body, err := json.Marshal(yields); err == nil {
		if err := s.persist.SaveKV(ctx, kvYields, string(body)); err != nil {
			log.Printf("free-proxy yields save: %v", err)
		}
	}
}

const (
	kvGroups = "groups"
	kvYields = "yields"
)

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
	if body, err := s.persist.LoadKV(ctx, kvGroups); err == nil {
		var groups []ProxyGroup
		if json.Unmarshal([]byte(body), &groups) == nil {
			if s.groups == nil {
				s.groups = map[string]ProxyGroup{}
			}
			for _, g := range groups {
				s.groups[g.Name] = g
			}
		}
	}
	if body, err := s.persist.LoadKV(ctx, kvYields); err == nil {
		var yields map[string][]SourceYieldRecord
		if json.Unmarshal([]byte(body), &yields) == nil && yields != nil {
			s.yields = yields
		}
	}
	return nil
}
