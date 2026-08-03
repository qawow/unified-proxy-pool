package freproxies

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyScored     = "upp:proxies:scored"
	keyRaw        = "upp:proxies:raw"
	keyAll        = "upp:proxies:all"
	keyMetaPrefix = "upp:proxies:meta:"
	keyScraper    = "upp:scrapers:stats"
	keyDisabled   = "upp:scrapers:disabled"
	keyEvents     = "upp:events"
)

const (
	// MaxRawProxies caps unvalidated proxies to protect memory/redis.
	MaxRawProxies = 4000
	// MaxValidatedProxies soft cap for scored set size during trim.
	MaxValidatedProxies = 2000
)

type Store interface {
	Backend() string
	Ping(ctx context.Context) error
	Close() error
	AddRaw(ctx context.Context, proxies []Proxy) (added int, err error)
	List(ctx context.Context, filter ListFilter) (ListResult, error)
	Get(ctx context.Context, addr string) (Proxy, error)
	Delete(ctx context.Context, addr string) error
	Random(ctx context.Context, protocol string) (Proxy, error)
	RandomN(ctx context.Context, protocol string, n int) ([]Proxy, error)
	Count(ctx context.Context) (total, validated, raw int64, err error)
	MarkValidated(ctx context.Context, addr string, latencyMS int64, ok bool) error
	ListRaw(ctx context.Context, limit int64) ([]Proxy, error)
	ListValidated(ctx context.Context, limit int64) ([]Proxy, error)
	Trim(ctx context.Context) error
	SaveScraperStat(ctx context.Context, stat ScraperStat) error
	ListScraperStats(ctx context.Context) (map[string]ScraperStat, error)
	SetScraperEnabled(ctx context.Context, name string, enabled bool) error
	IsScraperEnabled(ctx context.Context, name string, defaultEnabled bool) (bool, error)
	PushEvent(ctx context.Context, msg string) error
	RecentEvents(ctx context.Context, limit int64) ([]string, error)
	RegionTop(ctx context.Context, limit int) ([]RegionCount, error)
	Queues(ctx context.Context) (ValidatorQueues, error)
	AvgScore(ctx context.Context) (float64, error)
	UpdateRegion(ctx context.Context, addr, region string) error
}

type redisStore struct {
	rdb *redis.Client
}

func OpenRedis(addr, password string, db int) (Store, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &redisStore{rdb: rdb}, nil
}

func (s *redisStore) Backend() string { return "redis" }

func (s *redisStore) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

func (s *redisStore) Close() error { return s.rdb.Close() }

func (s *redisStore) metaKey(addr string) string {
	return keyMetaPrefix + addr
}

// mgetProxies loads many metas in one pipeline round-trip.
func (s *redisStore) mgetProxies(ctx context.Context, addrs []string) []Proxy {
	if len(addrs) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(addrs))
	for i, addr := range addrs {
		cmds[i] = pipe.Get(ctx, s.metaKey(addr))
	}
	_, _ = pipe.Exec(ctx)
	out := make([]Proxy, 0, len(addrs))
	for i, cmd := range cmds {
		raw, err := cmd.Bytes()
		if err != nil {
			continue
		}
		var p Proxy
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if p.Addr == "" {
			p.Addr = addrs[i]
		}
		out = append(out, p)
	}
	return out
}

func (s *redisStore) AddRaw(ctx context.Context, proxies []Proxy) (int, error) {
	if len(proxies) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	// normalize + collect candidate addrs
	type cand struct {
		p    Proxy
		addr string
	}
	cands := make([]cand, 0, len(proxies))
	addrs := make([]string, 0, len(proxies))
	for _, p := range proxies {
		addr := normalizeAddr(p.Host, p.Port)
		if addr == "" {
			continue
		}
		cands = append(cands, cand{p: p, addr: addr})
		addrs = append(addrs, addr)
	}
	if len(cands) == 0 {
		return 0, nil
	}
	// batch existence check
	existPipe := s.rdb.Pipeline()
	existCmds := make([]*redis.BoolCmd, len(addrs))
	for i, addr := range addrs {
		existCmds[i] = existPipe.SIsMember(ctx, keyAll, addr)
	}
	_, err := existPipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	writePipe := s.rdb.Pipeline()
	added := 0
	for i, c := range cands {
		exists, err := existCmds[i].Result()
		if err != nil || exists {
			continue
		}
		p := c.p
		p.Addr = c.addr
		if p.Protocol == "" {
			p.Protocol = "http"
		}
		if p.Score == 0 {
			p.Score = ScoreInit
		}
		p.CreatedAt = now
		p.UpdatedAt = now
		raw, _ := json.Marshal(p)
		writePipe.SAdd(ctx, keyAll, c.addr)
		writePipe.ZAdd(ctx, keyRaw, redis.Z{Score: p.Score, Member: c.addr})
		writePipe.Set(ctx, s.metaKey(c.addr), raw, 0)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if _, err := writePipe.Exec(ctx); err != nil {
		return added, err
	}
	// Trim is caller's responsibility (scrape round end) — cheap opportunistic if huge batch
	if added > 500 {
		_ = s.Trim(ctx)
	}
	return added, nil
}

func (s *redisStore) Get(ctx context.Context, addr string) (Proxy, error) {
	raw, err := s.rdb.Get(ctx, s.metaKey(addr)).Bytes()
	if err != nil {
		return Proxy{}, err
	}
	var p Proxy
	if err := json.Unmarshal(raw, &p); err != nil {
		return Proxy{}, err
	}
	return p, nil
}

func (s *redisStore) saveMeta(ctx context.Context, p Proxy) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.metaKey(p.Addr), raw, 0).Err()
}

func (s *redisStore) Delete(ctx context.Context, addr string) error {
	pipe := s.rdb.Pipeline()
	pipe.SRem(ctx, keyAll, addr)
	pipe.ZRem(ctx, keyRaw, addr)
	pipe.ZRem(ctx, keyScored, addr)
	pipe.Del(ctx, s.metaKey(addr))
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisStore) ListRaw(ctx context.Context, limit int64) ([]Proxy, error) {
	if limit <= 0 {
		limit = 200
	}
	members, err := s.rdb.ZRange(ctx, keyRaw, 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	return s.mgetProxies(ctx, members), nil
}

func (s *redisStore) ListValidated(ctx context.Context, limit int64) ([]Proxy, error) {
	if limit <= 0 {
		limit = 100
	}
	// oldest-ish first for revalidation: low end of zset by score then we sort by LastCheck
	members, err := s.rdb.ZRange(ctx, keyScored, 0, limit*2-1).Result()
	if err != nil {
		return nil, err
	}
	out := s.mgetProxies(ctx, members)
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastCheck.Before(out[j].LastCheck)
	})
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *redisStore) MarkValidated(ctx context.Context, addr string, latencyMS int64, ok bool) error {
	p, err := s.Get(ctx, addr)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	p.LastCheck = now
	p.UpdatedAt = now
	p.LatencyMS = latencyMS
	if ok {
		p.Validated = true
		p.Score = ScoreMax
		p.FailCount = 0
		pipe := s.rdb.Pipeline()
		pipe.ZRem(ctx, keyRaw, addr)
		pipe.ZAdd(ctx, keyScored, redis.Z{Score: p.Score, Member: addr})
		if err := s.saveMeta(ctx, p); err != nil {
			return err
		}
		_, err = pipe.Exec(ctx)
		return err
	}
	p.FailCount++
	p.Score = p.Score - 1
	if p.Score < ScoreMin {
		p.Score = ScoreMin
	}
	if p.Score <= 0 || p.FailCount >= 3 {
		return s.Delete(ctx, addr)
	}
	pipe := s.rdb.Pipeline()
	if p.Validated {
		pipe.ZAdd(ctx, keyScored, redis.Z{Score: p.Score, Member: addr})
	} else {
		pipe.ZAdd(ctx, keyRaw, redis.Z{Score: p.Score, Member: addr})
	}
	if err := s.saveMeta(ctx, p); err != nil {
		return err
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (s *redisStore) Random(ctx context.Context, protocol string) (Proxy, error) {
	items, err := s.RandomN(ctx, protocol, 1)
	if err != nil {
		return Proxy{}, err
	}
	if len(items) == 0 {
		return Proxy{}, fmt.Errorf("no proxy available")
	}
	return items[0], nil
}

func (s *redisStore) RandomN(ctx context.Context, protocol string, n int) ([]Proxy, error) {
	if n <= 0 {
		n = 1
	}
	// Small window: enough for filter + shuffle, avoid hydrating 500 metas.
	fetch := int64(n * 20)
	if fetch < 32 {
		fetch = 32
	}
	if fetch > 128 {
		fetch = 128
	}
	members, err := s.rdb.ZRevRange(ctx, keyScored, 0, fetch-1).Result()
	if err != nil {
		return nil, err
	}
	loaded := s.mgetProxies(ctx, members)
	candidates := make([]Proxy, 0, len(loaded))
	for _, p := range loaded {
		if protocol != "" && !strings.EqualFold(p.Protocol, protocol) {
			continue
		}
		if p.Validated || p.Score >= ScoreInit {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no proxy available")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		li, lj := candidates[i].LatencyMS, candidates[j].LatencyMS
		if li <= 0 {
			li = 1 << 30
		}
		if lj <= 0 {
			lj = 1 << 30
		}
		if li != lj {
			return li < lj
		}
		return candidates[i].Score > candidates[j].Score
	})
	window := len(candidates)
	if window > n*3 {
		window = n * 3
	}
	if window < 1 {
		window = 1
	}
	top := candidates[:window]
	rand.Shuffle(len(top), func(i, j int) { top[i], top[j] = top[j], top[i] })
	if n > len(top) {
		n = len(top)
	}
	return top[:n], nil
}

func (s *redisStore) Trim(ctx context.Context) error {
	rawCount, err := s.rdb.ZCard(ctx, keyRaw).Result()
	if err != nil {
		return err
	}
	if rawCount > MaxRawProxies {
		// drop lowest-score raw first (ZRANGE low to high)
		drop := rawCount - MaxRawProxies
		addrs, err := s.rdb.ZRange(ctx, keyRaw, 0, drop-1).Result()
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			_ = s.Delete(ctx, addr)
		}
	}
	scoredCount, err := s.rdb.ZCard(ctx, keyScored).Result()
	if err != nil {
		return err
	}
	if scoredCount > MaxValidatedProxies {
		drop := scoredCount - MaxValidatedProxies
		addrs, err := s.rdb.ZRange(ctx, keyScored, 0, drop-1).Result()
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			_ = s.Delete(ctx, addr)
		}
	}
	return nil
}

func (s *redisStore) Count(ctx context.Context) (total, validated, raw int64, err error) {
	total, err = s.rdb.SCard(ctx, keyAll).Result()
	if err != nil {
		return
	}
	validated, err = s.rdb.ZCard(ctx, keyScored).Result()
	if err != nil {
		return
	}
	raw, err = s.rdb.ZCard(ctx, keyRaw).Result()
	return
}

func matchListFilter(p Proxy, filter ListFilter) bool {
	if filter.OnlyOK && !p.Validated {
		return false
	}
	if filter.Source != "" && !strings.EqualFold(p.Source, filter.Source) {
		return false
	}
	if filter.Protocol != "" && !strings.EqualFold(p.Protocol, filter.Protocol) {
		return false
	}
	if filter.Region != "" && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(filter.Region)) {
		return false
	}
	if filter.MinScore > 0 && p.Score < filter.MinScore {
		return false
	}
	if q := strings.TrimSpace(strings.ToLower(filter.Query)); q != "" {
		hay := strings.ToLower(p.Addr + " " + p.Source + " " + p.Protocol + " " + p.Region)
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

func (s *redisStore) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Size <= 0 {
		filter.Size = 20
	}
	if filter.Size > 5000 {
		filter.Size = 5000
	}
	needFilter := filter.Source != "" || filter.Protocol != "" || filter.Region != "" ||
		filter.MinScore > 0 || strings.TrimSpace(filter.Query) != "" || !filter.OnlyOK

	scoredCard, _ := s.rdb.ZCard(ctx, keyScored).Result()
	rawCard, _ := s.rdb.ZCard(ctx, keyRaw).Result()

	// Fast path: only_ok, no extra filters — true Redis pagination on scored set.
	if filter.OnlyOK && !needFilter {
		total := scoredCard
		start := int64((filter.Page - 1) * filter.Size)
		if start >= total {
			return ListResult{Items: []Proxy{}, Total: total, Page: filter.Page, Size: filter.Size}, nil
		}
		end := start + int64(filter.Size) - 1
		members, err := s.rdb.ZRevRange(ctx, keyScored, start, end).Result()
		if err != nil {
			return ListResult{}, err
		}
		return ListResult{Items: s.mgetProxies(ctx, members), Total: total, Page: filter.Page, Size: filter.Size}, nil
	}

	// Filtered path: load bounded window (not entire DB), mget, filter, paginate in memory.
	// Cap hydrate to protect Redis under large pools.
	maxHydrate := int64(800)
	if filter.Size >= 1000 {
		maxHydrate = 5000 // export path
	}
	scoredN := scoredCard
	if scoredN > maxHydrate {
		scoredN = maxHydrate
	}
	var members []string
	if scoredN > 0 {
		ms, err := s.rdb.ZRevRange(ctx, keyScored, 0, scoredN-1).Result()
		if err != nil {
			return ListResult{}, err
		}
		members = append(members, ms...)
	}
	if !filter.OnlyOK {
		rawN := rawCard
		remain := maxHydrate - int64(len(members))
		if rawN > remain {
			rawN = remain
		}
		if rawN > 0 {
			ms, err := s.rdb.ZRevRange(ctx, keyRaw, 0, rawN-1).Result()
			if err != nil {
				return ListResult{}, err
			}
			seen := map[string]struct{}{}
			for _, m := range members {
				seen[m] = struct{}{}
			}
			for _, m := range ms {
				if _, ok := seen[m]; ok {
					continue
				}
				members = append(members, m)
			}
		}
	}

	loaded := s.mgetProxies(ctx, members)
	items := make([]Proxy, 0, len(loaded))
	for _, p := range loaded {
		if matchListFilter(p, filter) {
			items = append(items, p)
		}
	}
	// Approximate total: filtered count in window; if window saturated use card estimate
	total := int64(len(items))
	if int64(len(members)) >= maxHydrate {
		if filter.OnlyOK {
			total = scoredCard
		} else {
			total = scoredCard + rawCard
		}
	}
	start := (filter.Page - 1) * filter.Size
	if start >= len(items) {
		return ListResult{Items: []Proxy{}, Total: total, Page: filter.Page, Size: filter.Size}, nil
	}
	end := start + filter.Size
	if end > len(items) {
		end = len(items)
	}
	return ListResult{Items: items[start:end], Total: total, Page: filter.Page, Size: filter.Size}, nil
}

func (s *redisStore) SaveScraperStat(ctx context.Context, stat ScraperStat) error {
	raw, err := json.Marshal(stat)
	if err != nil {
		return err
	}
	return s.rdb.HSet(ctx, keyScraper, stat.Name, raw).Err()
}

func (s *redisStore) ListScraperStats(ctx context.Context) (map[string]ScraperStat, error) {
	data, err := s.rdb.HGetAll(ctx, keyScraper).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ScraperStat, len(data))
	for k, v := range data {
		var st ScraperStat
		if err := json.Unmarshal([]byte(v), &st); err != nil {
			continue
		}
		out[k] = st
	}
	return out, nil
}

func (s *redisStore) SetScraperEnabled(ctx context.Context, name string, enabled bool) error {
	if enabled {
		return s.rdb.SRem(ctx, keyDisabled, name).Err()
	}
	return s.rdb.SAdd(ctx, keyDisabled, name).Err()
}

func (s *redisStore) IsScraperEnabled(ctx context.Context, name string, defaultEnabled bool) (bool, error) {
	disabled, err := s.rdb.SIsMember(ctx, keyDisabled, name).Result()
	if err != nil {
		return defaultEnabled, err
	}
	if disabled {
		return false, nil
	}
	return defaultEnabled, nil
}

func (s *redisStore) PushEvent(ctx context.Context, msg string) error {
	pipe := s.rdb.Pipeline()
	pipe.LPush(ctx, keyEvents, fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339), msg))
	pipe.LTrim(ctx, keyEvents, 0, 49)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisStore) RecentEvents(ctx context.Context, limit int64) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.rdb.LRange(ctx, keyEvents, 0, limit-1).Result()
}

func (s *redisStore) RegionTop(ctx context.Context, limit int) ([]RegionCount, error) {
	// Sample top scored only — good enough for dashboard, avoids full-set hydrate.
	members, err := s.rdb.ZRevRange(ctx, keyScored, 0, 299).Result()
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, p := range s.mgetProxies(ctx, members) {
		region := p.Region
		if region == "" {
			region = "unknown"
		}
		counts[region]++
	}
	out := make([]RegionCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, RegionCount{Region: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *redisStore) Queues(ctx context.Context) (ValidatorQueues, error) {
	raw, err := s.rdb.ZCard(ctx, keyRaw).Result()
	if err != nil {
		return ValidatorQueues{}, err
	}
	validated, err := s.rdb.ZCard(ctx, keyScored).Result()
	if err != nil {
		return ValidatorQueues{}, err
	}
	// Score buckets from zset scores only (no meta GET). Protocol/fail sample top 300.
	members, err := s.rdb.ZRevRangeWithScores(ctx, keyScored, 0, -1).Result()
	if err != nil {
		return ValidatorQueues{}, err
	}
	buckets := map[string]int64{"0-20": 0, "21-50": 0, "51-80": 0, "81-100": 0}
	sampleAddrs := make([]string, 0, 300)
	for i, z := range members {
		switch {
		case z.Score <= 20:
			buckets["0-20"]++
		case z.Score <= 50:
			buckets["21-50"]++
		case z.Score <= 80:
			buckets["51-80"]++
		default:
			buckets["81-100"]++
		}
		if i < 300 {
			if addr, ok := z.Member.(string); ok {
				sampleAddrs = append(sampleAddrs, addr)
			}
		}
	}
	protocols := map[string]int64{}
	sourceFails := map[string]int64{}
	for _, p := range s.mgetProxies(ctx, sampleAddrs) {
		protocols[p.Protocol]++
		if p.FailCount > 0 {
			sourceFails[p.Source] += int64(p.FailCount)
		}
	}
	fails := make([]SourceFail, 0, len(sourceFails))
	for k, v := range sourceFails {
		fails = append(fails, SourceFail{Name: k, Fails: v})
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].Fails > fails[j].Fails })
	if len(fails) > 10 {
		fails = fails[:10]
	}
	return ValidatorQueues{
		RawCount:       raw,
		ValidatedCount: validated,
		ScoreBuckets:   buckets,
		ProtocolCounts: protocols,
		FailTopSources: fails,
	}, nil
}

func (s *redisStore) AvgScore(ctx context.Context) (float64, error) {
	members, err := s.rdb.ZRevRangeWithScores(ctx, keyScored, 0, -1).Result()
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		return 0, nil
	}
	var sum float64
	for _, z := range members {
		sum += z.Score
	}
	return sum / float64(len(members)), nil
}

func (s *redisStore) UpdateRegion(ctx context.Context, addr, region string) error {
	p, err := s.Get(ctx, addr)
	if err != nil {
		return err
	}
	p.Region = region
	p.UpdatedAt = time.Now().UTC()
	return s.saveMeta(ctx, p)
}

func normalizeAddr(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 || port > 65535 {
		return ""
	}
	return host + ":" + strconv.Itoa(port)
}

// memoryStore is used when Redis is unavailable so the panel still works.
type memoryStore struct {
	mu        sync.RWMutex
	proxies   map[string]Proxy
	raw       map[string]struct{}
	scored    map[string]struct{}
	stats     map[string]ScraperStat
	disabled  map[string]struct{}
	events    []string
}

func NewMemoryStore() Store {
	return &memoryStore{
		proxies:  map[string]Proxy{},
		raw:      map[string]struct{}{},
		scored:   map[string]struct{}{},
		stats:    map[string]ScraperStat{},
		disabled: map[string]struct{}{},
	}
}

func (s *memoryStore) Backend() string                  { return "memory" }
func (s *memoryStore) Ping(ctx context.Context) error   { return nil }
func (s *memoryStore) Close() error                     { return nil }

func (s *memoryStore) AddRaw(ctx context.Context, proxies []Proxy) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := 0
	now := time.Now().UTC()
	for _, p := range proxies {
		addr := normalizeAddr(p.Host, p.Port)
		if addr == "" {
			continue
		}
		if _, ok := s.proxies[addr]; ok {
			continue
		}
		// soft refuse when raw already huge; prefer keeping room for validated
		if len(s.raw) >= MaxRawProxies && len(s.scored) > 50 {
			continue
		}
		p.Addr = addr
		if p.Protocol == "" {
			p.Protocol = "http"
		}
		if p.Score == 0 {
			p.Score = ScoreInit
		}
		p.CreatedAt = now
		p.UpdatedAt = now
		s.proxies[addr] = p
		s.raw[addr] = struct{}{}
		added++
	}
	// inline trim without re-lock
	if len(s.raw) > MaxRawProxies {
		addrs := make([]string, 0, len(s.raw))
		for addr := range s.raw {
			addrs = append(addrs, addr)
		}
		sort.Slice(addrs, func(i, j int) bool {
			return s.proxies[addrs[i]].CreatedAt.Before(s.proxies[addrs[j]].CreatedAt)
		})
		drop := len(s.raw) - MaxRawProxies
		for i := 0; i < drop && i < len(addrs); i++ {
			addr := addrs[i]
			delete(s.proxies, addr)
			delete(s.raw, addr)
		}
	}
	return added, nil
}

func (s *memoryStore) Get(ctx context.Context, addr string) (Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proxies[addr]
	if !ok {
		return Proxy{}, redis.Nil
	}
	return p, nil
}

func (s *memoryStore) Delete(ctx context.Context, addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.proxies, addr)
	delete(s.raw, addr)
	delete(s.scored, addr)
	return nil
}

func (s *memoryStore) ListRaw(ctx context.Context, limit int64) ([]Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Proxy, 0, len(s.raw))
	for addr := range s.raw {
		out = append(out, s.proxies[addr])
		if int64(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

func (s *memoryStore) ListValidated(ctx context.Context, limit int64) ([]Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]Proxy, 0, len(s.scored))
	for addr := range s.scored {
		out = append(out, s.proxies[addr])
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastCheck.Before(out[j].LastCheck)
	})
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) MarkValidated(ctx context.Context, addr string, latencyMS int64, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, exists := s.proxies[addr]
	if !exists {
		return redis.Nil
	}
	now := time.Now().UTC()
	p.LastCheck = now
	p.UpdatedAt = now
	p.LatencyMS = latencyMS
	if ok {
		p.Validated = true
		p.Score = ScoreMax
		p.FailCount = 0
		delete(s.raw, addr)
		s.scored[addr] = struct{}{}
		s.proxies[addr] = p
		return nil
	}
	p.FailCount++
	p.Score--
	if p.Score <= 0 || p.FailCount >= 3 {
		delete(s.proxies, addr)
		delete(s.raw, addr)
		delete(s.scored, addr)
		return nil
	}
	s.proxies[addr] = p
	return nil
}

func (s *memoryStore) Random(ctx context.Context, protocol string) (Proxy, error) {
	items, err := s.RandomN(ctx, protocol, 1)
	if err != nil {
		return Proxy{}, err
	}
	if len(items) == 0 {
		return Proxy{}, fmt.Errorf("no proxy available")
	}
	return items[0], nil
}

func (s *memoryStore) RandomN(ctx context.Context, protocol string, n int) ([]Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n <= 0 {
		n = 1
	}
	candidates := make([]Proxy, 0, len(s.scored))
	for addr := range s.scored {
		p := s.proxies[addr]
		if protocol != "" && !strings.EqualFold(p.Protocol, protocol) {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		// best-effort: raw pool sample
		for addr := range s.raw {
			p := s.proxies[addr]
			if protocol != "" && !strings.EqualFold(p.Protocol, protocol) {
				continue
			}
			candidates = append(candidates, p)
			if len(candidates) >= 200 {
				break
			}
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no proxy available")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		li, lj := candidates[i].LatencyMS, candidates[j].LatencyMS
		if li <= 0 {
			li = 1 << 30
		}
		if lj <= 0 {
			lj = 1 << 30
		}
		if li != lj {
			return li < lj
		}
		return candidates[i].Score > candidates[j].Score
	})
	window := len(candidates)
	if window > n*3 {
		window = n * 3
	}
	if window < 1 {
		window = 1
	}
	top := candidates[:window]
	rand.Shuffle(len(top), func(i, j int) { top[i], top[j] = top[j], top[i] })
	if n > len(top) {
		n = len(top)
	}
	return top[:n], nil
}

func (s *memoryStore) Trim(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.raw) > MaxRawProxies {
		addrs := make([]string, 0, len(s.raw))
		for addr := range s.raw {
			addrs = append(addrs, addr)
		}
		// drop oldest-ish by CreatedAt
		sort.Slice(addrs, func(i, j int) bool {
			return s.proxies[addrs[i]].CreatedAt.Before(s.proxies[addrs[j]].CreatedAt)
		})
		drop := len(s.raw) - MaxRawProxies
		for i := 0; i < drop && i < len(addrs); i++ {
			addr := addrs[i]
			delete(s.proxies, addr)
			delete(s.raw, addr)
			delete(s.scored, addr)
		}
	}
	if len(s.scored) > MaxValidatedProxies {
		addrs := make([]string, 0, len(s.scored))
		for addr := range s.scored {
			addrs = append(addrs, addr)
		}
		sort.Slice(addrs, func(i, j int) bool {
			return s.proxies[addrs[i]].Score < s.proxies[addrs[j]].Score
		})
		drop := len(s.scored) - MaxValidatedProxies
		for i := 0; i < drop && i < len(addrs); i++ {
			addr := addrs[i]
			delete(s.proxies, addr)
			delete(s.raw, addr)
			delete(s.scored, addr)
		}
	}
	return nil
}

func (s *memoryStore) Count(ctx context.Context) (total, validated, raw int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.proxies)), int64(len(s.scored)), int64(len(s.raw)), nil
}

func (s *memoryStore) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Size <= 0 {
		filter.Size = 20
	}
	items := make([]Proxy, 0, len(s.proxies))
	for _, p := range s.proxies {
		if filter.OnlyOK && !p.Validated {
			continue
		}
		if filter.Source != "" && !strings.EqualFold(p.Source, filter.Source) {
			continue
		}
		if filter.Protocol != "" && !strings.EqualFold(p.Protocol, filter.Protocol) {
			continue
		}
		if filter.Region != "" && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(filter.Region)) {
			continue
		}
		if filter.MinScore > 0 && p.Score < filter.MinScore {
			continue
		}
		if q := strings.TrimSpace(strings.ToLower(filter.Query)); q != "" {
			hay := strings.ToLower(p.Addr + " " + p.Source + " " + p.Protocol + " " + p.Region)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		items = append(items, p)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	total := int64(len(items))
	start := (filter.Page - 1) * filter.Size
	if start >= len(items) {
		return ListResult{Items: []Proxy{}, Total: total, Page: filter.Page, Size: filter.Size}, nil
	}
	end := start + filter.Size
	if end > len(items) {
		end = len(items)
	}
	return ListResult{Items: items[start:end], Total: total, Page: filter.Page, Size: filter.Size}, nil
}

func (s *memoryStore) SaveScraperStat(ctx context.Context, stat ScraperStat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats[stat.Name] = stat
	return nil
}

func (s *memoryStore) ListScraperStats(ctx context.Context) (map[string]ScraperStat, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]ScraperStat, len(s.stats))
	for k, v := range s.stats {
		out[k] = v
	}
	return out, nil
}

func (s *memoryStore) SetScraperEnabled(ctx context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled {
		delete(s.disabled, name)
	} else {
		s.disabled[name] = struct{}{}
	}
	return nil
}

func (s *memoryStore) IsScraperEnabled(ctx context.Context, name string, defaultEnabled bool) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.disabled[name]; ok {
		return false, nil
	}
	return defaultEnabled, nil
}

func (s *memoryStore) PushEvent(ctx context.Context, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append([]string{fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339), msg)}, s.events...)
	if len(s.events) > 50 {
		s.events = s.events[:50]
	}
	return nil
}

func (s *memoryStore) RecentEvents(ctx context.Context, limit int64) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || int(limit) > len(s.events) {
		limit = int64(len(s.events))
	}
	out := make([]string, limit)
	copy(out, s.events[:limit])
	return out, nil
}

func (s *memoryStore) RegionTop(ctx context.Context, limit int) ([]RegionCount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int64{}
	for addr := range s.scored {
		region := s.proxies[addr].Region
		if region == "" {
			region = "unknown"
		}
		counts[region]++
	}
	out := make([]RegionCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, RegionCount{Region: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) Queues(ctx context.Context) (ValidatorQueues, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buckets := map[string]int64{"0-20": 0, "21-50": 0, "51-80": 0, "81-100": 0}
	protocols := map[string]int64{}
	sourceFails := map[string]int64{}
	for addr := range s.scored {
		p := s.proxies[addr]
		switch {
		case p.Score <= 20:
			buckets["0-20"]++
		case p.Score <= 50:
			buckets["21-50"]++
		case p.Score <= 80:
			buckets["51-80"]++
		default:
			buckets["81-100"]++
		}
		protocols[p.Protocol]++
		if p.FailCount > 0 {
			sourceFails[p.Source] += int64(p.FailCount)
		}
	}
	fails := make([]SourceFail, 0, len(sourceFails))
	for k, v := range sourceFails {
		fails = append(fails, SourceFail{Name: k, Fails: v})
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].Fails > fails[j].Fails })
	return ValidatorQueues{
		RawCount:       int64(len(s.raw)),
		ValidatedCount: int64(len(s.scored)),
		ScoreBuckets:   buckets,
		ProtocolCounts: protocols,
		FailTopSources: fails,
	}, nil
}

func (s *memoryStore) AvgScore(ctx context.Context) (float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.scored) == 0 {
		return 0, nil
	}
	var sum float64
	for addr := range s.scored {
		sum += s.proxies[addr].Score
	}
	return sum / float64(len(s.scored)), nil
}

func (s *memoryStore) UpdateRegion(ctx context.Context, addr, region string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.proxies[addr]
	if !ok {
		return fmt.Errorf("not found")
	}
	p.Region = region
	p.UpdatedAt = time.Now().UTC()
	s.proxies[addr] = p
	return nil
}
