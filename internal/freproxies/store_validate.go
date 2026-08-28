package freproxies

import (
	"math/rand"
	"sort"
	"time"
)

// rawValidateCooldown is how long a just-tested raw proxy is skipped so the
// next click does not retest the same batch. Failures stay in the raw zset
// until FailCount>=3; without this they sat at ZRange(0, N) forever.
const rawValidateCooldown = 10 * time.Minute

func selectValidateRaw(items []Proxy, limit int64, now time.Time, cooldown time.Duration) []Proxy {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	fresh := make([]Proxy, 0, len(items))
	stale := make([]Proxy, 0)
	for _, p := range items {
		if p.Addr == "" {
			continue
		}
		if p.LastCheck.IsZero() || now.Sub(p.LastCheck) >= cooldown {
			fresh = append(fresh, p)
		} else {
			stale = append(stale, p)
		}
	}
	rand.Shuffle(len(fresh), func(i, j int) { fresh[i], fresh[j] = fresh[j], fresh[i] })
	if int64(len(fresh)) >= limit {
		return fresh[:limit]
	}
	sort.Slice(stale, func(i, j int) bool {
		return stale[i].LastCheck.Before(stale[j].LastCheck)
	})
	out := fresh
	need := int(limit) - len(out)
	if need > len(stale) {
		need = len(stale)
	}
	return append(out, stale[:need]...)
}
