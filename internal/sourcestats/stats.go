package sourcestats

import (
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/db"
)

const windowCap = 50

type Stat struct {
	Name          string    `json:"name"`
	OK            int64     `json:"ok"`
	Fail          int64     `json:"fail"`
	LatencySumMS  int64     `json:"-"`
	AvgLatencyMS  float64   `json:"avg_latency_ms"`
	SuccessRate   float64   `json:"success_rate"`
	RecentOK      int64     `json:"recent_ok"`
	RecentFail    int64     `json:"recent_fail"`
	RecentRate    float64   `json:"recent_rate"`
	DisabledUntil time.Time `json:"disabled_until,omitempty"`
	AutoDisabled  bool      `json:"auto_disabled"`
	strikes       int
	window        []bool // true = ok, newest last
}

type Registry struct {
	mu    sync.RWMutex
	m     map[string]*Stat
	db    *db.Store
	dirty atomic.Bool
	now   func() time.Time
}

func New() *Registry {
	return &Registry{m: map[string]*Stat{}, now: func() time.Time { return time.Now().UTC() }}
}

var Default = New()

func (r *Registry) nowUTC() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now().UTC()
}

func (r *Registry) Record(source string, ok bool, latencyMS int64) {
	if source == "" {
		source = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, exists := r.m[source]
	if !exists {
		st = &Stat{Name: source}
		r.m[source] = st
	}
	if ok {
		st.OK++
		if latencyMS > 0 {
			st.LatencySumMS += latencyMS
		}
	} else {
		st.Fail++
	}
	st.pushWindow(ok)
	total := st.OK + st.Fail
	if total > 0 {
		st.SuccessRate = float64(st.OK) / float64(total)
	}
	if st.OK > 0 {
		st.AvgLatencyMS = float64(st.LatencySumMS) / float64(st.OK)
	}
	r.markDirty()
}

func (st *Stat) pushWindow(ok bool) {
	st.window = append(st.window, ok)
	if len(st.window) > windowCap {
		st.window = append([]bool(nil), st.window[len(st.window)-windowCap:]...)
	}
	st.recountRecent()
}

func (st *Stat) recountRecent() {
	var ok, fail int64
	for _, v := range st.window {
		if v {
			ok++
		} else {
			fail++
		}
	}
	st.RecentOK, st.RecentFail = ok, fail
	if n := ok + fail; n > 0 {
		st.RecentRate = float64(ok) / float64(n)
	} else {
		st.RecentRate = 0
	}
}

func (r *Registry) Evaluate(minSamples int, minRate float64) {
	if minRate <= 0 {
		return
	}
	if minSamples < 5 {
		minSamples = 5
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowUTC()
	for _, st := range r.m {
		recentN := int(st.RecentOK + st.RecentFail)
		expired := st.AutoDisabled && !st.DisabledUntil.IsZero() && !now.Before(st.DisabledUntil)

		if recentN < minSamples {
			if expired {
				st.AutoDisabled = false
				st.DisabledUntil = time.Time{}
			}
			continue
		}

		if st.RecentRate < minRate {
			if st.AutoDisabled && now.Before(st.DisabledUntil) {
				// Still in the penalty window: do not refresh TTL.
				continue
			}
			st.AutoDisabled = true
			st.strikes++
			if st.strikes < 1 {
				st.strikes = 1
			}
			shift := st.strikes - 1
			if shift > 4 {
				shift = 4
			}
			ttl := time.Duration(1<<uint(shift)) * time.Hour
			if ttl > 24*time.Hour {
				ttl = 24 * time.Hour
			}
			st.DisabledUntil = now.Add(ttl)
			continue
		}

		if st.AutoDisabled && (expired || st.DisabledUntil.IsZero()) {
			st.AutoDisabled = false
			st.DisabledUntil = time.Time{}
			st.strikes = 0
		}
	}
	r.markDirty()
}

func (r *Registry) IsDisabled(source string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.m[source]
	if !ok {
		return false
	}
	return st.effectivelyDisabled(r.nowUTC())
}

func (st *Stat) effectivelyDisabled(now time.Time) bool {
	if st == nil || !st.AutoDisabled {
		return false
	}
	if st.DisabledUntil.IsZero() {
		return true
	}
	return now.Before(st.DisabledUntil)
}

func (r *Registry) List() []Stat {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.nowUTC()
	out := make([]Stat, 0, len(r.m))
	for _, st := range r.m {
		cp := *st
		cp.window = nil
		cp.AutoDisabled = st.effectivelyDisabled(now)
		out = append(out, cp)
	}
	return out
}

func (r *Registry) Snapshot(name string) (Stat, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.m[name]
	if !ok {
		return Stat{}, false
	}
	cp := *st
	cp.window = nil
	cp.AutoDisabled = st.effectivelyDisabled(r.nowUTC())
	return cp, true
}

func (r *Registry) Reenable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.m[name]; ok {
		st.AutoDisabled = false
		st.DisabledUntil = time.Time{}
		st.strikes = 0
		r.markDirty()
	}
}

func (r *Registry) RecentCounts() map[string][2]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][2]int, len(r.m))
	for name, st := range r.m {
		out[name] = [2]int{int(st.RecentOK), int(st.RecentFail)}
	}
	return out
}
