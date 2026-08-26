package chanpolicy

import (
	"sync"
	"time"
)

// LogEntry is one observed or reported outcome, kept so the panel can show
// *why* a ban fired rather than only that it did.
//
// This is a ring, not a database: it is for recent forensics, not audit. A
// restart clears it. Bans themselves persist separately.
type LogEntry struct {
	At        time.Time `json:"at"`
	Channel   string    `json:"channel"`
	Addr      string    `json:"addr"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status,omitempty"`
	Err       string    `json:"err,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Reported  bool      `json:"reported,omitempty"`
	Banned    bool      `json:"banned,omitempty"` // this outcome itself triggered a ban
	Reason    string    `json:"reason,omitempty"`
}

const defaultLogCap = 500

type logRing struct {
	mu    sync.RWMutex
	items []LogEntry
	cap   int
}

func newLogRing(cap int) *logRing {
	if cap <= 0 {
		cap = defaultLogCap
	}
	return &logRing{cap: cap, items: make([]LogEntry, 0, cap)}
}

func (r *logRing) add(e LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, e)
	if len(r.items) > r.cap {
		r.items = r.items[len(r.items)-r.cap:]
	}
}

// list returns the most recent entries, newest last, optionally filtered to
// one channel. limit <= 0 means the whole ring.
func (r *logRing) list(channel string, limit int) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.items
	if channel != "" {
		filtered := make([]LogEntry, 0, len(src))
		for _, e := range src {
			if e.Channel == channel {
				filtered = append(filtered, e)
			}
		}
		src = filtered
	}
	if limit <= 0 || limit > len(src) {
		limit = len(src)
	}
	if limit == 0 {
		return []LogEntry{}
	}
	out := make([]LogEntry, limit)
	copy(out, src[len(src)-limit:])
	return out
}

func (r *logRing) clear() {
	r.mu.Lock()
	r.items = r.items[:0]
	r.mu.Unlock()
}

func (r *logRing) len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}
