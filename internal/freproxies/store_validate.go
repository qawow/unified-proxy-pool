package freproxies

import (
	"sort"
	"time"
)

const failDeleteAfter = 2

func retryDueUnix(failCount int, now time.Time) float64 {
	d := 5 * time.Minute
	if failCount >= 2 {
		d = 15 * time.Minute
	}
	return float64(now.Add(d).Unix())
}

// rawValidateCooldown skips a just-tested address so the next batch of a
// scan picks never-checked / older ones instead of the same slice.
// Never-checked (LastCheck zero) always sort first, so a 4000-raw pool is
// walked in ~limit-sized steps rather than randomly resampled.
const rawValidateCooldown = 90 * time.Second

func countUnchecked(items []Proxy) int {
	n := 0
	for _, p := range items {
		if p.Addr != "" && p.LastCheck.IsZero() {
			n++
		}
	}
	return n
}

func selectValidateRaw(items []Proxy, limit int64, now time.Time, cooldown time.Duration) []Proxy {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	never := make([]Proxy, 0, len(items))
	due := make([]Proxy, 0)
	recent := make([]Proxy, 0)
	for _, p := range items {
		if p.Addr == "" {
			continue
		}
		if p.LastCheck.IsZero() {
			never = append(never, p)
			continue
		}
		if cooldown <= 0 || now.Sub(p.LastCheck) >= cooldown {
			due = append(due, p)
		} else {
			recent = append(recent, p)
		}
	}
	sort.Slice(never, func(i, j int) bool { return never[i].Addr < never[j].Addr })
	sort.Slice(due, func(i, j int) bool { return due[i].LastCheck.Before(due[j].LastCheck) })
	out := append(never, due...)
	if int64(len(out)) >= limit {
		return out[:limit]
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].LastCheck.Before(recent[j].LastCheck) })
	need := int(limit) - len(out)
	if need > len(recent) {
		need = len(recent)
	}
	return append(out, recent[:need]...)
}

func pickRawScan(all []Proxy, limit int64, now time.Time, cooldown time.Duration) (batch []Proxy, total, unchecked int) {
	total = len(all)
	unchecked = countUnchecked(all)
	batch = selectValidateRaw(all, limit, now, cooldown)
	return
}
