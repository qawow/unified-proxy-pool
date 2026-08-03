package validator

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/sourcestats"
	"unified-proxy-pool/internal/webhook"
)

type Service struct {
	mu        sync.RWMutex
	cfg       config.App
	free      *freproxies.Service
	extraURLs []string
	minRate   float64
	minSample int
}

func New(cfg config.App, free *freproxies.Service) *Service {
	return &Service{cfg: cfg, free: free, minRate: 0.15, minSample: 20}
}

func (s *Service) ApplyFreeConfig(url string, timeoutMS, concurrency int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if url != "" {
		s.cfg.FreeValidateURL = url
	}
	if timeoutMS > 0 {
		s.cfg.FreeValidateTimeoutMS = timeoutMS
	}
	if concurrency > 0 {
		s.cfg.FreeValidateConcurrency = concurrency
	}
}

func (s *Service) ApplyExtras(urls []string, minRate float64, minSample int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraURLs = append([]string{}, urls...)
	if minRate > 0 {
		s.minRate = minRate
	}
	if minSample > 0 {
		s.minSample = minSample
	}
}

func (s *Service) validateURLs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	seen := map[string]bool{}
	for _, u := range s.extraURLs {
		if u != "" && !seen[u] {
			out = append(out, u)
			seen[u] = true
		}
	}
	if s.cfg.FreeValidateURL != "" && !seen[s.cfg.FreeValidateURL] {
		out = append(out, s.cfg.FreeValidateURL)
	}
	if len(out) == 0 {
		out = []string{"https://www.gstatic.com/generate_204"}
	}
	return out
}

func (s *Service) ValidateBatch(ctx context.Context, limit int64) {
	if s == nil || s.free == nil {
		return
	}
	if limit <= 0 {
		limit = 200
	}
	store := s.free.Store()

	rawLimit := limit * 7 / 10
	if rawLimit < 1 {
		rawLimit = limit
	}
	reLimit := limit - rawLimit
	if reLimit < 0 {
		reLimit = 0
	}

	raw, err := store.ListRaw(ctx, rawLimit)
	if err != nil {
		log.Printf("validator list raw: %v", err)
		DefaultLogs.Add("fail", "", "list raw failed: "+err.Error(), "", 0)
		return
	}
	var scored []freproxies.Proxy
	if reLimit > 0 {
		scored, _ = store.ListValidated(ctx, reLimit)
	}

	batch := append(raw, scored...)
	if len(batch) == 0 {
		DefaultLogs.Add("info", "", "校验批次跳过：无待验代理", "", 0)
		return
	}

	s.mu.RLock()
	concurrency := s.cfg.FreeValidateConcurrency
	timeoutMS := s.cfg.FreeValidateTimeoutMS
	minRate := s.minRate
	minSample := s.minSample
	s.mu.RUnlock()
	urls := s.validateURLs()
	if concurrency <= 0 {
		concurrency = 16
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	DefaultLogs.SetRunning(true)
	DefaultLogs.Add("info", "", "开始校验 batch="+strconv.Itoa(len(batch))+
		" raw="+strconv.Itoa(len(raw))+" recheck="+strconv.Itoa(len(scored))+
		" urls="+strconv.Itoa(len(urls)), "", 0)
	defer DefaultLogs.SetRunning(false)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var okCount, failCount int
	var mu sync.Mutex
	for _, p := range batch {
		p := p
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			runCtx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
			defer cancel()
			var lastErr error
			var latency int64
			okResult := false
			for _, u := range urls {
				item, err := s.free.TestProxyOpts(runCtx, p.Addr, u, timeout, false)
				if err == nil {
					okResult = true
					latency = item.LatencyMS
					lastErr = nil
					break
				}
				lastErr = err
			}
			mu.Lock()
			if okResult {
				okCount++
				DefaultLogs.Add("ok", p.Addr, "通过", p.Source, latency)
				sourcestats.Default.Record(p.Source, true, latency)
			} else {
				failCount++
				msg := "失败"
				if lastErr != nil {
					msg = lastErr.Error()
				}
				DefaultLogs.Add("fail", p.Addr, msg, p.Source, 0)
				sourcestats.Default.Record(p.Source, false, 0)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	sourcestats.Default.Evaluate(minSample, minRate)
	summary := "validate batch done ok=" + strconv.Itoa(okCount) + " fail=" + strconv.Itoa(failCount) +
		" raw=" + strconv.Itoa(len(raw)) + " recheck=" + strconv.Itoa(len(scored))
	_ = store.PushEvent(ctx, summary)
	DefaultLogs.Add("info", "", summary, "", 0)
	s.free.NotifyValidateBatch(okCount, failCount)
	log.Printf("validate batch: ok=%d fail=%d (raw=%d recheck=%d)", okCount, failCount, len(raw), len(scored))
	if okCount == 0 && failCount > 0 {
		webhook.Default.Notify("validate_all_fail", map[string]any{"fail": failCount})
	}
}
