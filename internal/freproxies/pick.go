package freproxies

import (
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// Selection strategies. These mirror chanpolicy's constants; freproxies does not
// import that package so the dependency stays one-way (app wires them together).
const (
	StrategyWeighted = "weighted"
	StrategyRandom   = "random"
	StrategyRR       = "rr"
)

// ChannelPolicy is the slice of the channel-ban registry that selection needs.
// Keeping it an interface lets tests drive selection without the real registry.
type ChannelPolicy interface {
	// BanSet returns addr -> expiry for every proxy currently sidelined on this
	// channel. One call per pick, rather than one lookup per candidate.
	BanSet(channel string) map[string]time.Time
}

// PickOptions describes one selection request.
type PickOptions struct {
	Protocol string
	Region   string
	Family   string
	N        int

	// Channel scopes the pick: proxies banned for this channel are skipped.
	// Empty means no channel filtering at all.
	Channel string

	// Strategy is weighted (default), random, or rr.
	Strategy string

	// NoCooldown disables recently-served suppression for this pick.
	NoCooldown bool
}

// PickResult carries the chosen proxies plus why the choice looks like it does.
type PickResult struct {
	Items   []Proxy `json:"items"`
	Channel string  `json:"channel,omitempty"`
	// Relaxed is true when channel bans had to be ignored because honouring them
	// would have served nothing. Callers surface this rather than silently
	// pretending the ban was respected.
	Relaxed bool `json:"relaxed,omitempty"`
	// Strategy is the strategy actually applied.
	Strategy string `json:"strategy,omitempty"`
}

func (o PickOptions) strategy() string {
	switch o.Strategy {
	case StrategyRandom, StrategyRR, StrategyWeighted:
		return o.Strategy
	default:
		return StrategyWeighted
	}
}

// pickState holds the per-process selection memory: which proxies went out
// recently, and where each channel's round-robin cursor sits.
type pickState struct {
	mu sync.Mutex

	// served is addr -> last handed-out time, used for cooldown.
	served map[string]time.Time
	// cursor is channel -> round-robin position.
	cursor map[string]uint64

	cooldown time.Duration
}

// Ceilings on the selection memory. Both maps are keyed by data the operator does
// not control (proxy addresses, destination channels), so both need a bound.
const (
	maxServedTracked = 8192
	maxCursors       = 1024
	defaultCooldown  = 30 * time.Second
	// cooldownWeightDivisor is how much a recently-served proxy is deprioritised.
	// It is a divisor rather than an exclusion on purpose: on a small pool, hard
	// exclusion would leave nothing to serve.
	cooldownWeightDivisor = 8
)

func newPickState() *pickState {
	return &pickState{
		served:   make(map[string]time.Time),
		cursor:   make(map[string]uint64),
		cooldown: defaultCooldown,
	}
}

func (p *pickState) setCooldown(d time.Duration) {
	p.mu.Lock()
	p.cooldown = d
	p.mu.Unlock()
}

// recentlyServed reports which of the given addresses are inside the cooldown
// window, in one pass under one lock.
func (p *pickState) recentlyServed(items []Proxy, now time.Time) map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cooldown <= 0 {
		return nil
	}
	var out map[string]bool
	for _, it := range items {
		if at, ok := p.served[it.Addr]; ok && now.Sub(at) < p.cooldown {
			if out == nil {
				out = make(map[string]bool, len(items))
			}
			out[it.Addr] = true
		}
	}
	return out
}

// markServed records that these proxies were handed out.
func (p *pickState) markServed(items []Proxy, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range items {
		p.served[it.Addr] = now
	}
	if len(p.served) > maxServedTracked {
		// Drop everything already outside the window; if that is not enough, reset.
		// Cooldown is an optimisation, so losing the history costs a little
		// clustering, not correctness.
		for addr, at := range p.served {
			if now.Sub(at) >= p.cooldown {
				delete(p.served, addr)
			}
		}
		if len(p.served) > maxServedTracked {
			p.served = make(map[string]time.Time, len(items))
			for _, it := range items {
				p.served[it.Addr] = now
			}
		}
	}
}

// nextCursor advances and returns the round-robin position for a channel.
func (p *pickState) nextCursor(channel string, step int) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cursor) > maxCursors {
		p.cursor = make(map[string]uint64)
	}
	cur := p.cursor[channel]
	p.cursor[channel] = cur + uint64(step)
	return cur
}

// proxyWeight scores a proxy's desirability. Higher is likelier to be picked.
//
// The three factors answer three different questions, and all three matter:
// Score is the validator's verdict, LatencyMS is how slow it is when it works,
// and FailCount is how often it has let us down. A proxy scoring 100 at 3s is
// not obviously better than one scoring 60 at 100ms, so neither term alone is
// enough to order the pool.
//
// The floor of 1 is deliberate: a zero weight would make a proxy unreachable,
// and on a pool where everything scores badly that means serving nothing.
func proxyWeight(p Proxy, recentlyServed bool) float64 {
	w := p.Score
	if w < 1 {
		w = 1
	}
	if p.LatencyMS > 0 {
		// Reciprocal decay: 100ms keeps ~91%, 1s ~50%, 5s ~17%. Gentler than
		// 1/latency, which would make anything above a second effectively unusable.
		w *= 1000 / (1000 + float64(p.LatencyMS))
	}
	if p.FailCount > 0 {
		w /= 1 + float64(p.FailCount)
	}
	if recentlyServed {
		w /= cooldownWeightDivisor
	}
	if w < 0.0001 {
		w = 0.0001
	}
	return w
}

// weightedSample draws up to n distinct proxies with probability proportional to
// weight, without replacement.
//
// O(n*k) on a candidate window of tens of items, which is cheaper than the Redis
// round-trip that produced the window.
func weightedSample(items []Proxy, n int, recent map[string]bool, rng *rand.Rand) []Proxy {
	if n >= len(items) {
		// Nothing to choose: return everything, but still ordered by weight so the
		// caller's failover attempts start with the most promising proxy.
		out := append([]Proxy(nil), items...)
		sort.SliceStable(out, func(i, j int) bool {
			return proxyWeight(out[i], recent[out[i].Addr]) > proxyWeight(out[j], recent[out[j].Addr])
		})
		return out
	}

	pool := append([]Proxy(nil), items...)
	weights := make([]float64, len(pool))
	total := 0.0
	for i, p := range pool {
		weights[i] = proxyWeight(p, recent[p.Addr])
		total += weights[i]
	}

	out := make([]Proxy, 0, n)
	for len(out) < n && len(pool) > 0 {
		target := rng.Float64() * total
		idx := len(pool) - 1
		acc := 0.0
		for i, w := range weights {
			acc += w
			if acc >= target {
				idx = i
				break
			}
		}
		out = append(out, pool[idx])
		total -= weights[idx]
		// Swap-delete; order in the remaining pool does not matter.
		last := len(pool) - 1
		pool[idx], weights[idx] = pool[last], weights[last]
		pool, weights = pool[:last], weights[:last]
		if total <= 0 {
			// Floating-point drift, or every remaining weight is at the floor.
			total = 0
			for _, w := range weights {
				total += w
			}
			if total <= 0 {
				break
			}
		}
	}
	return out
}

// rrSelect walks the candidates in a stable order from the channel's own cursor,
// so consecutive picks for one channel sweep the pool instead of re-drawing, and
// two channels never tread on each other's position.
func rrSelect(items []Proxy, n int, channel string, state *pickState) []Proxy {
	if len(items) == 0 {
		return nil
	}
	ordered := append([]Proxy(nil), items...)
	// Sort by address so the walk order is stable across picks even though the
	// candidate window arrives shuffled.
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Addr < ordered[j].Addr })
	if n > len(ordered) {
		n = len(ordered)
	}
	start := state.nextCursor(channel, n)
	out := make([]Proxy, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ordered[int((start+uint64(i))%uint64(len(ordered)))])
	}
	return out
}

// applyStrategy reduces a candidate window to at most n proxies.
func (s *Service) applyStrategy(items []Proxy, opt PickOptions) []Proxy {
	n := opt.N
	if n <= 0 {
		n = 1
	}
	if len(items) == 0 {
		return nil
	}
	now := time.Now()
	var recent map[string]bool
	if !opt.NoCooldown {
		recent = s.picks.recentlyServed(items, now)
	}

	var out []Proxy
	switch opt.strategy() {
	case StrategyRandom:
		out = append([]Proxy(nil), items...)
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		if n < len(out) {
			out = out[:n]
		}
	case StrategyRR:
		key := opt.Channel
		if key == "" {
			key = "_global"
		}
		out = rrSelect(items, n, key, s.picks)
	default:
		out = weightedSample(items, n, recent, rand.New(rand.NewSource(time.Now().UnixNano())))
	}
	s.picks.markServed(out, now)
	return out
}

// normalizeStrategy maps a user-supplied strategy name onto a known one.
func normalizeStrategy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case StrategyRandom, "shuffle":
		return StrategyRandom
	case StrategyRR, "roundrobin", "round_robin", "round-robin":
		return StrategyRR
	case StrategyWeighted, "weight", "score":
		return StrategyWeighted
	default:
		return ""
	}
}
