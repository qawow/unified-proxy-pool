package scheduler

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/validator"
)

type IntervalProvider func(ctx context.Context) (scrapeSec, validateSec int)

type Scheduler struct {
	cfg       config.App
	free      *freproxies.Service
	validator *validator.Service
	mu        sync.Mutex
	scraping  bool

	intervals IntervalProvider
	// last run unix for dynamic interval
	lastScrapeUnix   atomic.Int64
	lastValidateUnix atomic.Int64
	validateRounds   atomic.Int64
	lastTuneUnix     atomic.Int64
}

func New(cfg config.App, free *freproxies.Service, val *validator.Service) *Scheduler {
	return &Scheduler{cfg: cfg, free: free, validator: val}
}

func (s *Scheduler) SetIntervalProvider(p IntervalProvider) {
	s.intervals = p
}

func (s *Scheduler) scrapeEvery() time.Duration {
	sec := s.cfg.ScrapeIntervalSec
	if s.intervals != nil {
		if sc, _ := s.intervals(context.Background()); sc > 0 {
			sec = sc
		}
	}
	d := time.Duration(sec) * time.Second
	if d < 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func (s *Scheduler) validateEvery() time.Duration {
	sec := s.cfg.ValidateIntervalSec
	if s.intervals != nil {
		if _, v := s.intervals(context.Background()); v > 0 {
			sec = v
		}
	}
	d := time.Duration(sec) * time.Second
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.free == nil || !s.cfg.FreeProxyEnabled {
		log.Printf("free-proxy scheduler disabled")
		return
	}

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(8 * time.Second):
		}
		s.scrapeOnce(ctx)
		s.validateOnce(ctx)

		// 30s tick; decide by settings intervals so UI changes apply without restart
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		log.Printf("free-proxy scheduler started dynamic intervals scrape=%s validate=%s", s.scrapeEvery(), s.validateEvery())
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				now := time.Now().Unix()
				if now-s.lastScrapeUnix.Load() >= int64(s.scrapeEvery().Seconds()) {
					s.scrapeOnce(ctx)
				}
				if now-s.lastValidateUnix.Load() >= int64(s.validateEvery().Seconds()) {
					s.validateOnce(ctx)
				}
			}
		}
	}()
}

func (s *Scheduler) scrapeOnce(ctx context.Context) {
	s.mu.Lock()
	if s.scraping {
		s.mu.Unlock()
		return
	}
	s.scraping = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.scraping = false
		s.mu.Unlock()
		s.lastScrapeUnix.Store(time.Now().Unix())
	}()

	runCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	log.Printf("free-proxy scrape round start")
	s.free.RunAllEnabled(runCtx)
	_ = s.free.Store().Trim(runCtx)
	log.Printf("free-proxy scrape round done")
}

func (s *Scheduler) validateOnce(ctx context.Context) {
	if s.validator == nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	s.validator.ValidateBatch(runCtx, 400)
	s.lastValidateUnix.Store(time.Now().Unix())
	if s.free != nil {
		s.free.RecordValidateYield(runCtx, s.validator.LastSourceBatch())
		n := s.validateRounds.Add(1)
		now := time.Now().Unix()
		if n%3 == 0 && now-s.lastTuneUnix.Load() >= 3600 {
			applied, abort, err := s.free.TuneFromYield(runCtx)
			s.lastTuneUnix.Store(now)
			if err != nil {
				log.Printf("sourcetune: %v", err)
			} else if abort != "" {
				log.Printf("sourcetune skipped: %s", abort)
			} else if applied > 0 {
				log.Printf("sourcetune applied %d source toggle(s)", applied)
			}
		}
	}
}

func (s *Scheduler) TriggerScrape(ctx context.Context) {
	go s.scrapeOnce(context.Background())
}

func (s *Scheduler) TriggerValidate(ctx context.Context) {
	go s.validateOnce(context.Background())
}

func (s *Scheduler) ApplyValidateConfig(url string, timeoutMS, concurrency int) {
	if s != nil && s.validator != nil {
		s.validator.ApplyFreeConfig(url, timeoutMS, concurrency)
	}
}

func (s *Scheduler) ApplyValidateExtras(urls []string, minRate float64, minSample int) {
	if s != nil && s.validator != nil {
		s.validator.ApplyExtras(urls, minRate, minSample)
	}
}
