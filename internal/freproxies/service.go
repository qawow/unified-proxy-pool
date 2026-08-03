package freproxies

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/events"
	"unified-proxy-pool/internal/netutil"
)

type Service struct {
	store     Store
	registry  *crawlers.Registry
	client    *crawlers.HTTPClient
	events    *events.Broker
	redisOK   bool
	geoLookup func(ctx context.Context, ip string) (string, error)
	// optional filters
	blocked        func(addr string) bool
	sourceDisabled func(source string) bool
	geoQueue       chan string
	hot            *HotCache
	overviewMu     sync.Mutex
	overviewCache  Overview
	overviewAt     time.Time
}

func NewService(store Store, registry *crawlers.Registry, broker *events.Broker, redisOK bool) *Service {
	s := &Service{
		store:    store,
		registry: registry,
		client:   crawlers.NewHTTPClient(15 * time.Second),
		events:   broker,
		redisOK:  redisOK,
		geoQueue: make(chan string, 2048),
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

func (s *Service) SetBlockedFn(fn func(addr string) bool) {
	s.blocked = fn
}

func (s *Service) SetSourceDisabledFn(fn func(source string) bool) {
	s.sourceDisabled = fn
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
	return s.store.List(ctx, filter)
}

func (s *Service) Random(ctx context.Context, protocol string) (Proxy, error) {
	return s.RandomFilter(ctx, protocol, "")
}

func (s *Service) RandomFilter(ctx context.Context, protocol, region string) (Proxy, error) {
	// try a few times to skip blacklist
	for i := 0; i < 8; i++ {
		p, err := s.store.Random(ctx, protocol)
		if err != nil {
			return Proxy{}, err
		}
		if s.blocked != nil && s.blocked(p.Addr) {
			continue
		}
		if s.sourceDisabled != nil && s.sourceDisabled(p.Source) {
			continue
		}
		if region != "" && p.Region != "" && !strings.Contains(strings.ToLower(p.Region), strings.ToLower(region)) {
			continue
		}
		return p, nil
	}
	items, err := s.PickValidatedNFilter(ctx, protocol, region, 1)
	if err != nil {
		return Proxy{}, err
	}
	return items[0], nil
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
	items, err := crawlers.FetchAll(ctx, s.client, c)
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
			Source:   c.Name(),
			Score:    ScoreInit,
		})
	}
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
		_, _ = s.store.AddRaw(ctx, []Proxy{p})
		p, _ = s.store.Get(ctx, addr)
	}
	latency, okResult := checkHTTPProxy(ctx, p, validateURL, timeout)
	_ = s.store.MarkValidated(ctx, addr, latency, okResult)
	if okResult && s.geoLookup != nil && host != "" {
		go func(ip string, a string) {
			gctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			region, err := s.geoLookup(gctx, ip)
			if err != nil || region == "" {
				return
			}
			_ = s.store.UpdateRegion(context.Background(), a, region)
		}(host, addr)
	}
	updated, err := s.store.Get(ctx, addr)
	if err != nil {
		if !okResult {
			return p, fmt.Errorf("proxy validation failed")
		}
		return p, err
	}
	if publish {
		s.publish("validate.finished", map[string]any{"addr": addr, "ok": okResult, "latency_ms": latency})
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

func (s *Service) publish(typ string, data map[string]any) {
	if s.events == nil {
		return
	}
	s.events.Publish(typ, data)
}

func splitAddr(addr string) (string, int, bool) {
	parts := strings.Split(strings.TrimSpace(addr), ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, false
	}
	return parts[0], port, true
}
