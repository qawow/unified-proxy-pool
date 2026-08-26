package sourcestats

import (
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/db"
)

type Stat struct {
	Name          string    `json:"name"`
	OK            int64     `json:"ok"`
	Fail          int64     `json:"fail"`
	LatencySumMS  int64     `json:"-"`
	AvgLatencyMS  float64   `json:"avg_latency_ms"`
	SuccessRate   float64   `json:"success_rate"`
	DisabledUntil time.Time `json:"disabled_until,omitempty"`
	AutoDisabled  bool      `json:"auto_disabled"`
}

type Registry struct {
	mu    sync.RWMutex
	m     map[string]*Stat
	db    *db.Store
	dirty atomic.Bool
}

func New() *Registry {
	return &Registry{m: map[string]*Stat{}}
}

var Default = New()

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
	total := st.OK + st.Fail
	if total > 0 {
		st.SuccessRate = float64(st.OK) / float64(total)
	}
	if st.OK > 0 {
		st.AvgLatencyMS = float64(st.LatencySumMS) / float64(st.OK)
	}
	r.markDirty()
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
	now := time.Now().UTC()
	for _, st := range r.m {
		total := st.OK + st.Fail
		if int(total) < minSamples {
			continue
		}
		if st.SuccessRate < minRate {
			st.AutoDisabled = true
			st.DisabledUntil = now.Add(1 * time.Hour)
		} else if st.AutoDisabled && now.After(st.DisabledUntil) {
			st.AutoDisabled = false
			st.DisabledUntil = time.Time{}
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
	if !st.AutoDisabled {
		return false
	}
	if time.Now().UTC().After(st.DisabledUntil) {
		return false
	}
	return true
}

func (r *Registry) List() []Stat {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Stat, 0, len(r.m))
	for _, st := range r.m {
		cp := *st
		out = append(out, cp)
	}
	return out
}

func (r *Registry) Reenable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.m[name]; ok {
		st.AutoDisabled = false
		st.DisabledUntil = time.Time{}
		r.markDirty()
	}
}
