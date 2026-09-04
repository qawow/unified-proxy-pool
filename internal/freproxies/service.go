package freproxies

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/geoip"
	"unified-proxy-pool/internal/netutil"
)

type Service struct {
	store     Store
	registry  *crawlers.Registry
	clientMu  sync.RWMutex
	client    *crawlers.HTTPClient
	fallback  *crawlers.HTTPClient
	events    *events.Broker
	redisOK   bool
	geoLookup func(ctx context.Context, ip string) (string, error)
	geoSvc    *geoip.Service
	// optional filters
	blocked        func(addr string) bool
	sourceDisabled func(source string) bool
	// channelPolicy supplies per-channel temporary bans. Optional: nil means
	// selection ignores channels entirely.
	channelPolicy   ChannelPolicy
	defaultStrategy string
	picks           *pickState
	geoQueue        chan string
	hot             *HotCache
	overviewMu      sync.Mutex
	overviewCache   Overview
	overviewAt      time.Time
}

func NewService(store Store, registry *crawlers.Registry, broker *events.Broker, redisOK bool) *Service {
	s := &Service{
		store:    store,
		registry: registry,
		client:   crawlers.NewHTTPClient(15 * time.Second),
		events:   broker,
		redisOK:  redisOK,
		geoQueue: make(chan string, 2048),
		picks:    newPickState(),
	}
	s.hot = NewHotCache(store, 96, 3*time.Second)
	return s
}

func (s *Service) StartHotCache(ctx context.Context) {
	if s != nil && s.hot != nil {
		s.hot.Start(ctx)
	}
}

func (s *Service) Hot() *HotCache {
	if s == nil {
		return nil
	}
	return s.hot
}

func (s *Service) SetGeoLookup(fn func(ctx context.Context, ip string) (string, error)) {
	s.geoLookup = fn
}

// SetScrapeProxy rebuilds the primary crawler client. raw is already resolved
// (http://127.0.0.1:7893, socks5://…, "none", or empty for env/direct).
func (s *Service) SetScrapeProxy(raw string) {
	if s == nil {
		return
	}
	s.clientMu.Lock()
	s.client = crawlers.NewHTTPClientWithProxy(20*time.Second, raw)
	s.clientMu.Unlock()
}

// SetScrapeFallback is used when scrape_proxy is empty: try direct first
// (jsdmirror works on CN WAN), and only on network errors retry via this
// proxy (usually the published mihomo mixed port).
func (s *Service) SetScrapeFallback(raw string) {
	if s == nil {
		return
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if strings.TrimSpace(raw) == "" {
		s.fallback = nil
		return
	}
	s.fallback = crawlers.NewHTTPClientWithProxy(20*time.Second, raw)
}

func (s *Service) scrapeClients() (primary, fallback *crawlers.HTTPClient) {
	if s == nil {
		return nil, nil
	}
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.client, s.fallback
}

func (s *Service) SetGeoService(g *geoip.Service) {
	s.geoSvc = g
}

func (s *Service) SetBlockedFn(fn func(addr string) bool) {
	s.blocked = fn
}

func (s *Service) SetSourceDisabledFn(fn func(source string) bool) {
	s.sourceDisabled = fn
}

// SetChannelPolicy attaches the per-channel ban registry used by selection.
func (s *Service) SetChannelPolicy(cp ChannelPolicy) {
	if s != nil {
		s.channelPolicy = cp
	}
}

// SetPickDefaults hot-applies the panel's selection settings.
func (s *Service) SetPickDefaults(strategy string, cooldown time.Duration) {
	if s == nil {
		return
	}
	if v := normalizeStrategy(strategy); v != "" {
		s.defaultStrategy = v
	}
	if s.picks != nil && cooldown >= 0 {
		s.picks.setCooldown(cooldown)
	}
}

// StartGeoWorker fills region asynchronously for IPs enqueued after validate.
func (s *Service) StartGeoWorker(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ip := <-s.geoQueue:
				if s.geoLookup == nil || ip == "" {
					continue
				}
				region, err := s.geoLookup(ctx, ip)
				if err != nil || region == "" {
					continue
				}
				// best-effort: find proxies with this host and patch via re-get/mark is heavy;
				// store region on next validate. Push event for visibility.
				_ = s.store.PushEvent(ctx, "geoip "+ip+" -> "+region)
			}
		}
	}()
}

func (s *Service) EnqueueGeo(ip string) {
	if s == nil || s.geoQueue == nil || ip == "" {
		return
	}
	select {
	case s.geoQueue <- ip:
	default:
	}
}

func (s *Service) Registry() *crawlers.Registry { return s.registry }

func (s *Service) Store() Store { return s.store }

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	s.overviewMu.Lock()
	if time.Since(s.overviewAt) < 3*time.Second && s.overviewCache.TotalProxies > 0 {
		cp := s.overviewCache
		s.overviewMu.Unlock()
		return cp, nil
	}
	s.overviewMu.Unlock()

	total, validated, raw, err := s.store.Count(ctx)
	if err != nil {
		return Overview{}, err
	}
	avg, _ := s.store.AvgScore(ctx)
	regions, _ := s.store.RegionTop(ctx, 8)
	ev, _ := s.store.RecentEvents(ctx, 15)
	queues, _ := s.store.Queues(ctx)
	enabled := 0
	for _, c := range s.registry.All() {
		ok, _ := s.store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
		if ok {
			enabled++
		}
	}
	lanIPs := netutil.LANIPs()
	panelHint := ""
	if ip := netutil.PreferLANIP(); ip != "" {
		panelHint = "http://" + ip + ":7891"
	}
	ov := Overview{
		TotalProxies:     total,
		ValidatedProxies: validated,
		RawProxies:       raw,
		SourceCount:      len(s.registry.All()),
		EnabledSources:   enabled,
		AvgScore:         avg,
		RedisOK:          s.redisOK,
		Backend:          s.store.Backend(),
		RegionTop:        regions,
		RecentEvents:     ev,
		QueueDepth: map[string]int64{
			"raw":       queues.RawCount,
			"validated": queues.ValidatedCount,
		},
		LANIPs:    lanIPs,
		PanelHint: panelHint,
	}
	s.overviewMu.Lock()
	s.overviewCache = ov
	s.overviewAt = time.Now()
	s.overviewMu.Unlock()
	return ov, nil
}

func (s *Service) ListProxies(ctx context.Context, filter ListFilter) (ListResult, error) {
	// Resolve a named group into an inline rule so stores stay group-unaware.
	resolved, err := s.ResolveGroupFilter(ctx, filter)
	if err != nil {
		return ListResult{}, err
	}
	return s.store.List(ctx, resolved)
}

func (s *Service) Random(ctx context.Context, protocol string) (Proxy, error) {
	return s.RandomFilter(ctx, protocol, "")
}

func (s *Service) RandomFilter(ctx context.Context, protocol, region string) (Proxy, error) {
	return s.RandomFamilyFilter(ctx, protocol, region, "")
}

// RandomFamilyFilter picks a random proxy, optionally constrained to one IP family.
//
// It is a thin wrapper over Pick so leftover callers get the same weighting,
// cooldown and channel-ban behaviour as the public get endpoint. The old
// store.Random fast path sampled a three-proxy latency window and ignored all
// of that, which is why a family-less request used to return the same handful
// of IPs over and over.
func (s *Service) RandomFamilyFilter(ctx context.Context, protocol, region, family string) (Proxy, error) {
	res, err := s.Pick(ctx, PickOptions{Protocol: protocol, Region: region, Family: family, N: 1})
	if err != nil {
		return Proxy{}, err
	}
	return res.Items[0], nil
}

// SubmitResult reports what a SubmitRaw call actually changed.
//
// Added alone is misleading once the raw pool is at MaxRawProxies: inserting N
// addresses then makes Trim evict N others, so the pool can report "added 4"
// while its total grows by 1. Callers that only surface Added will tell an
// operator the submission worked when it mostly displaced other proxies.
type SubmitResult struct {
	// Parsed is how many items survived normalization.
	Parsed int `json:"parsed"`
	// Added is how many addresses AddRaw actually inserted (duplicates skipped).
	Added int `json:"added"`
	// Duplicates is Parsed minus Added: already present in the pool.
	Duplicates int `json:"duplicates"`
	// Evicted is how many existing proxies Trim removed to stay under the cap.
	// Derived by conservation: (before + added) - after.
	Evicted int64 `json:"evicted"`
	// NetGrowth is the change in pool total. Zero with a positive Added means
	// the submission only displaced other proxies.
	NetGrowth int64 `json:"net_growth"`
	// RawAtCap is true when the raw pool is saturated, which is *why* eviction
	// happens; it tells the caller this is capacity pressure, not a bug.
	RawAtCap bool `json:"raw_at_cap"`
	// Blocked is how many well-formed items the country deny list dropped.
	Blocked int `json:"blocked,omitempty"`
}

// SubmitRaw adds a batch of raw addresses without validating them.
//
// Each item needs at least host:port; protocol defaults to "http" and source
// defaults to "external" when omitted.
//
// This is the service-layer entry point for POST /api/proxies/submit and
// POST /api/public/submit, which let scripts push scraped addresses without
// Redis access or a local CLI binary.
func (s *Service) SubmitRaw(ctx context.Context, items []Proxy, source string) (SubmitResult, error) {
	var res SubmitResult
	if len(items) == 0 {
		return res, nil
	}
	if source == "" {
		source = "external"
	}
	toAdd := make([]Proxy, 0, len(items))
	for _, p := range items {
		// Accept host+port or pre-formed addr; reject obviously malformed input.
		if p.Addr == "" && (p.Host == "" || p.Port <= 0) {
			continue
		}
		if p.Addr == "" {
			p.Addr = normalizeAddr(p.Host, p.Port)
		}
		if p.Addr == "" {
			continue
		}
		if p.Source == "" {
			p.Source = source
		}
		if p.Protocol == "" {
			p.Protocol = "http"
		}
		toAdd = append(toAdd, p)
	}
	res.Parsed = len(toAdd)
	toAdd, res.Blocked = s.dropBlocked(toAdd)
	if len(toAdd) == 0 {
		return res, nil
	}

	// Measure before/after so eviction can be derived rather than guessed.
	beforeTotal, _, _, _ := s.store.Count(ctx)

	added, err := s.store.AddRaw(ctx, toAdd)
	if err != nil {
		return res, err
	}
	res.Added = added
	res.Duplicates = res.Parsed - res.Blocked - added
	if res.Duplicates < 0 {
		res.Duplicates = 0
	}

	if err2 := s.store.Trim(ctx); err2 != nil {
		_ = err2 // trim failure is cosmetic; the counts below still reflect reality
	}

	afterTotal, _, afterRaw, _ := s.store.Count(ctx)
	res.NetGrowth = afterTotal - beforeTotal
	// Conservation: everything inserted that is not reflected in the total was
	// evicted. Delta-based detection alone misses the case where insertions and
	// evictions cancel exactly.
	if ev := (beforeTotal + int64(added)) - afterTotal; ev > 0 {
		res.Evicted = ev
	}
	res.RawAtCap = afterRaw >= MaxRawProxies

	if added > 0 {
		s.publish("freproxies.submitted", map[string]any{
			"added": added, "source": source, "evicted": res.Evicted,
		})
	}
	if res.Evicted > 0 {
		_ = s.store.PushEvent(ctx, fmt.Sprintf(
			"submit %s: added %d, evicted %d to stay under the raw cap (%d)",
			source, added, res.Evicted, MaxRawProxies))
	}
	return res, nil
}

// BatchTestResult is the outcome for one address in a BatchTest call.
type BatchTestResult struct {
	Addr      string `json:"addr"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BatchTest validates up to maxItems addresses concurrently.
//
// Each address that is not already in the store is added as raw first so that
// a successful test can be promoted to validated status. Dead addresses are
// removed, matching the behaviour of the periodic validator.
func (s *Service) BatchTest(ctx context.Context, addrs []string, validateURL string, timeout time.Duration, concurrency int) []BatchTestResult {
	const maxItems = 200
	if len(addrs) > maxItems {
		addrs = addrs[:maxItems]
	}
	if concurrency <= 0 || concurrency > 60 {
		concurrency = 20
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if validateURL == "" {
		validateURL = "http://httpbin.org/ip"
	}

	results := make([]BatchTestResult, len(addrs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, addr := range addrs {
		results[i].Addr = addr
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p, err := s.TestProxyOpts(ctx, addr, validateURL, timeout, false)
			if err != nil {
				results[i].OK = false
				results[i].Error = err.Error()
			} else {
				results[i].OK = true
				results[i].LatencyMS = p.LatencyMS
			}
		}(i, addr)
	}
	wg.Wait()

	ok, fail := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	s.NotifyValidateBatch(ok, fail)
	return results
}

func (s *Service) Delete(ctx context.Context, addr string) error {
	if err := s.store.Delete(ctx, addr); err != nil {
		return err
	}
	s.publish("freproxies.deleted", map[string]any{"addr": addr})
	return nil
}

func (s *Service) ListScrapers(ctx context.Context) ([]ScraperStat, error) {
	stats, _ := s.store.ListScraperStats(ctx)
	out := make([]ScraperStat, 0, len(s.registry.All()))
	for _, c := range s.registry.All() {
		st := stats[c.Name()]
		st.Name = c.Name()
		st.Protocol = c.Protocol()
		st.Fragile = c.Fragile()
		st.Builtin = crawlers.IsBuiltin(c)
		st.Format = crawlers.CrawlerFormat(c)
		st.URLs = append([]string(nil), c.URLs()...)
		if len(c.URLs()) > 0 {
			st.URLHint = c.URLs()[0]
		}
		enabled, _ := s.store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
		st.Enabled = enabled
		out = append(out, st)
	}
	return out, nil
}

func (s *Service) ToggleScraper(ctx context.Context, name string) (ScraperStat, error) {
	c, ok := s.registry.Get(name)
	if !ok {
		return ScraperStat{}, fmt.Errorf("unknown scraper: %s", name)
	}
	cur, _ := s.store.IsScraperEnabled(ctx, name, c.DefaultEnabled())
	if err := s.store.SetScraperEnabled(ctx, name, !cur); err != nil {
		return ScraperStat{}, err
	}
	list, err := s.ListScrapers(ctx)
	if err != nil {
		return ScraperStat{}, err
	}
	for _, item := range list {
		if item.Name == name {
			s.publish("scrapers.toggled", map[string]any{"name": name, "enabled": item.Enabled})
			return item, nil
		}
	}
	return ScraperStat{Name: name, Enabled: !cur}, nil
}

func (s *Service) RunScraper(ctx context.Context, name string) (ScraperStat, error) {
	c, ok := s.registry.Get(name)
	if !ok {
		return ScraperStat{}, fmt.Errorf("unknown scraper: %s", name)
	}
	return s.runOne(ctx, c)
}

func (s *Service) RunAllEnabled(ctx context.Context) {
	type job struct{ c crawlers.Crawler }
	var jobs []job
	for _, c := range s.registry.All() {
		enabled, err := s.store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
		if err != nil || !enabled {
			continue
		}
		jobs = append(jobs, job{c: c})
	}
	if len(jobs) == 0 {
		return
	}
	const workers = 6
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.runOne(ctx, j.c); err != nil {
				log.Printf("scraper %s: %v", j.c.Name(), err)
			}
		}()
	}
	wg.Wait()
}

func (s *Service) runOne(ctx context.Context, c crawlers.Crawler) (ScraperStat, error) {
	start := time.Now().UTC()
	primary, fallback := s.scrapeClients()
	items, err := crawlers.FetchAllWithFallback(ctx, primary, fallback, c)
	stat := ScraperStat{
		Name:      c.Name(),
		Protocol:  c.Protocol(),
		Fragile:   c.Fragile(),
		LastRunAt: start,
	}
	if len(c.URLs()) > 0 {
		stat.URLHint = c.URLs()[0]
	}
	enabled, _ := s.store.IsScraperEnabled(ctx, c.Name(), c.DefaultEnabled())
	stat.Enabled = enabled

	prev, _ := s.store.ListScraperStats(ctx)
	if old, ok := prev[c.Name()]; ok {
		stat.TotalOK = old.TotalOK
		stat.TotalFail = old.TotalFail
	}

	if err != nil {
		stat.LastFail = 1
		stat.LastError = err.Error()
		stat.TotalFail++
		_ = s.store.SaveScraperStat(ctx, stat)
		_ = s.store.PushEvent(ctx, fmt.Sprintf("scraper %s failed: %v", c.Name(), err))
		s.publish("scrapers.finished", map[string]any{"name": c.Name(), "ok": 0, "error": err.Error()})
		return stat, err
	}

	converted := make([]Proxy, 0, len(items))
	for _, item := range items {
		converted = append(converted, Proxy{
			Host:     item.Host,
			Port:     item.Port,
			Protocol: item.Protocol,
			Region:   item.Region,
			Source:   c.Name(),
			Score:    ScoreInit,
		})
	}
	converted, _ = s.dropBlocked(converted)
	added, addErr := s.store.AddRaw(ctx, converted)
	if addErr != nil {
		stat.LastError = addErr.Error()
		stat.LastFail = 1
		stat.TotalFail++
		_ = s.store.SaveScraperStat(ctx, stat)
		return stat, addErr
	}
	stat.LastOK = added
	stat.TotalOK += int64(added)
	stat.LastError = ""
	_ = s.store.SaveScraperStat(ctx, stat)
	_ = s.store.PushEvent(ctx, fmt.Sprintf("scraper %s fetched %d new", c.Name(), added))
	s.publish("scrapers.finished", map[string]any{"name": c.Name(), "ok": added})
	return stat, nil
}

func (s *Service) TestProxy(ctx context.Context, addr, validateURL string, timeout time.Duration) (Proxy, error) {
	return s.TestProxyOpts(ctx, addr, validateURL, timeout, true)
}

// TestProxyOpts validates a proxy. When publish is false, no per-item SSE is emitted (batch mode).
func (s *Service) TestProxyOpts(ctx context.Context, addr, validateURL string, timeout time.Duration, publish bool) (Proxy, error) {
	host, port, ok := splitAddr(addr)
	if !ok {
		return Proxy{}, fmt.Errorf("invalid addr")
	}
	p, err := s.store.Get(ctx, addr)
	if err != nil {
		p = Proxy{Addr: addr, Host: host, Port: port, Protocol: "http", Source: "manual", Score: ScoreInit}
		kept, n := s.dropBlocked([]Proxy{p})
		if n > 0 || len(kept) == 0 {
			return p, countryBlockedError(p.Region)
		}
		_, _ = s.store.AddRaw(ctx, kept)
		p, _ = s.store.Get(ctx, addr)
	} else if geoip.Active().Blocks(p.Region) || geoip.Active().BlockedNode(p.Host, "") {
		_ = s.store.Delete(ctx, addr)
		return p, countryBlockedError(p.Region)
	}
	latency, okResult := checkHTTPProxy(ctx, p, validateURL, timeout)
	region := p.Region
	// Via-proxy geo is expensive (ip-api ~45/min). If we already know a
	// country, trust it for this check; unknown region still goes through.
	if okResult && geoip.Active().CheckExit && geoip.Normalize(region) == "" {
		if exit := s.probeExitCountry(ctx, p, timeout); exit != "" {
			region = exit
		}
	}
	if okResult && region == "" && s.geoLookup != nil && host != "" {
		gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		hostRegion, gerr := s.geoLookup(gctx, host)
		cancel()
		if gerr == nil && hostRegion != "" {
			region = hostRegion
		}
	}
	if geoip.Active().Blocks(region) {
		_ = s.store.MarkValidated(ctx, addr, latency, false)
		_ = s.store.Delete(ctx, addr)
		if publish {
			s.publish("validate.finished", map[string]any{"addr": addr, "ok": false, "reason": "blocked_country", "region": region})
		}
		return p, countryBlockedError(region)
	}
	_ = s.store.MarkValidated(ctx, addr, latency, okResult)
	if okResult && region != "" {
		_ = s.store.UpdateRegion(ctx, addr, region)
	}
	updated, err := s.store.Get(ctx, addr)
	if err != nil {
		if !okResult {
			return p, fmt.Errorf("proxy validation failed")
		}
		return p, err
	}
	if publish {
		s.publish("validate.finished", map[string]any{"addr": addr, "ok": okResult, "latency_ms": latency, "region": region})
	}
	if !okResult {
		return updated, fmt.Errorf("proxy validation failed")
	}
	return updated, nil
}

// NotifyValidateBatch publishes a single SSE after a batch run.
func (s *Service) NotifyValidateBatch(okCount, failCount int) {
	s.publish("validate.batch", map[string]any{"ok": okCount, "fail": failCount})
}

func (s *Service) Queues(ctx context.Context) (ValidatorQueues, error) {
	return s.store.Queues(ctx)
}

// PurgeDead deletes every proxy sitting in the retry set (tested-dead, waiting
// to be tried again). Untested raw is left so the scanner can keep filling.
func (s *Service) PurgeDead(ctx context.Context) (int, error) {
	n, err := s.store.PurgeRetry(ctx)
	if err != nil {
		return n, err
	}
	if n > 0 {
		_ = s.store.PushEvent(ctx, fmt.Sprintf("purged %d dead retry proxies", n))
		s.publish("proxies.purged", map[string]any{"reason": "retry_dead", "count": n})
	}
	return n, nil
}

func (s *Service) publish(typ string, data map[string]any) {
	if s.events == nil {
		return
	}
	s.events.Publish(typ, data)
}

func splitAddr(addr string) (string, int, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, false
	}
	return host, port, true
}
