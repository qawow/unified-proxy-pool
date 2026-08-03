package freproxies

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// HotCache keeps a small in-process snapshot of validated proxies for dial/pick.
type HotCache struct {
	mu      sync.RWMutex
	items   []Proxy
	updated time.Time
	ttl     time.Duration
	size    int
	store   Store
	stop    chan struct{}
}

func NewHotCache(store Store, size int, refresh time.Duration) *HotCache {
	if size < 32 {
		size = 64
	}
	if refresh < time.Second {
		refresh = 3 * time.Second
	}
	h := &HotCache{
		ttl:   refresh,
		size:  size,
		store: store,
		stop:  make(chan struct{}),
	}
	return h
}

func (h *HotCache) Start(ctx context.Context) {
	if h == nil || h.store == nil {
		return
	}
	h.refresh(ctx)
	go func() {
		t := time.NewTicker(h.ttl)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.stop:
				return
			case <-t.C:
				h.refresh(ctx)
			}
		}
	}()
}

func (h *HotCache) refresh(ctx context.Context) {
	if h == nil || h.store == nil {
		return
	}
	items, err := h.store.ListValidated(ctx, int64(h.size))
	if err != nil || len(items) == 0 {
		// fallback RandomN window
		items, err = h.store.RandomN(ctx, "", h.size)
		if err != nil {
			return
		}
	}
	cp := make([]Proxy, len(items))
	copy(cp, items)
	h.mu.Lock()
	h.items = cp
	h.updated = time.Now()
	h.mu.Unlock()
}

func (h *HotCache) Invalidate(addr string) {
	if h == nil || addr == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.items[:0]
	for _, p := range h.items {
		if p.Addr != addr {
			out = append(out, p)
		}
	}
	h.items = out
}

func (h *HotCache) Snapshot() []Proxy {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Proxy, len(h.items))
	copy(out, h.items)
	return out
}

func (h *HotCache) Pick(n int, protocol, region string) []Proxy {
	if n <= 0 {
		n = 1
	}
	snap := h.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	filtered := make([]Proxy, 0, len(snap))
	for _, p := range snap {
		if protocol != "" && !strings.EqualFold(p.Protocol, protocol) {
			continue
		}
		if region != "" && p.Region != "" && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(region)) {
			continue
		}
		filtered = append(filtered, p)
	}
	if len(filtered) == 0 {
		filtered = snap
	}
	rand.Shuffle(len(filtered), func(i, j int) { filtered[i], filtered[j] = filtered[j], filtered[i] })
	if n > len(filtered) {
		n = len(filtered)
	}
	return filtered[:n]
}

// PickDistinct returns up to n proxies with unique hosts (and optionally regions).
func (h *HotCache) PickDistinct(n int, preferDistinctRegion bool, entryProto, exitProto, entryRegion, exitRegion string) []Proxy {
	snap := h.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	rand.Shuffle(len(snap), func(i, j int) { snap[i], snap[j] = snap[j], snap[i] })
	seenHost := map[string]struct{}{}
	seenRegion := map[string]struct{}{}
	out := make([]Proxy, 0, n)
	for _, p := range snap {
		host := p.Host
		if host == "" {
			if i := strings.LastIndex(p.Addr, ":"); i > 0 {
				host = p.Addr[:i]
			} else {
				host = p.Addr
			}
		}
		if _, ok := seenHost[host]; ok {
			continue
		}
		if preferDistinctRegion && p.Region != "" {
			if _, ok := seenRegion[strings.ToLower(p.Region)]; ok && len(out) > 0 {
				continue
			}
		}
		// position constraints
		idx := len(out)
		if idx == 0 && entryProto != "" && !strings.EqualFold(p.Protocol, entryProto) {
			continue
		}
		if idx == n-1 && n > 1 && exitProto != "" && !strings.EqualFold(p.Protocol, exitProto) {
			continue
		}
		if idx == 0 && entryRegion != "" && p.Region != "" &&
			!strings.Contains(strings.ToLower(p.Region), strings.ToLower(entryRegion)) {
			continue
		}
		if idx == n-1 && n > 1 && exitRegion != "" && p.Region != "" &&
			!strings.Contains(strings.ToLower(p.Region), strings.ToLower(exitRegion)) {
			continue
		}
		seenHost[host] = struct{}{}
		if p.Region != "" {
			seenRegion[strings.ToLower(p.Region)] = struct{}{}
		}
		out = append(out, p)
		if len(out) >= n {
			break
		}
	}
	return out
}
