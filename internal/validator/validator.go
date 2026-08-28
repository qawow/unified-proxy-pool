package validator

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"unified-proxy-pool/internal/config"
	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/sourcestats"
	"unified-proxy-pool/internal/webhook"
)

type BatchSummary struct {
	OK       int       `json:"ok"`
	Fail     int       `json:"fail"`
	Raw      int       `json:"raw"`
	Recheck  int       `json:"recheck"`
	Duration time.Duration
	At       time.Time `json:"at"`
}

type Progress struct {
	Running          bool
	Size             int
	OK               int
	Fail             int
	LifetimeOK       int64
	LifetimeFail     int64
	LifetimeBatches  int64
	RawTotal         int
	RawUnchecked     int
	Last             BatchSummary
	History          []BatchSummary
}

type Service struct {
	mu        sync.RWMutex
	cfg       config.App
	free      *freproxies.Service
	extraURLs []string
	minRate   float64
	minSample int
	lastBatch BatchSummary
	store     *db.Store
	batchSize int
	batchOK   int
	batchFail int
	lifeOK    int64
	lifeFail  int64
	lifeN      int64
	history      []BatchSummary
	sourceBatch  map[string][2]int
	rawTotal     int
	rawUnchecked int
}

var liveMu sync.RWMutex
var live *Service

func New(cfg config.App, free *freproxies.Service) *Service {
	s := &Service{cfg: cfg, free: free, minRate: 0.15, minSample: 20}
	liveMu.Lock()
	live = s
	liveMu.Unlock()
	return s
}

func LiveLastBatch() BatchSummary {
	liveMu.RLock()
	s := live
	liveMu.RUnlock()
	if s == nil {
		return BatchSummary{}
	}
	return s.LastBatch()
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

func (s *Service) SetDB(store *db.Store) {
	if s == nil || store == nil {
		return
	}
	ctx := context.Background()
	s.mu.Lock()
	s.store = store
	s.mu.Unlock()
	if lifeOK, lifeFail, lifeN, err := store.SumValidateBatches(ctx); err == nil {
		s.mu.Lock()
		s.lifeOK, s.lifeFail, s.lifeN = lifeOK, lifeFail, lifeN
		s.mu.Unlock()
	}
	if rows, err := store.ListValidateBatches(ctx, 20); err == nil {
		hist := make([]BatchSummary, 0, len(rows))
		for _, r := range rows {
			hist = append(hist, BatchSummary{
				OK: r.OK, Fail: r.Fail, Raw: r.Raw, Recheck: r.Recheck,
				Duration: time.Duration(r.DurationMS) * time.Millisecond, At: r.At,
			})
		}
		s.mu.Lock()
		s.history = hist
		if len(hist) > 0 {
			s.lastBatch = hist[0]
			s.batchOK = hist[0].OK
			s.batchFail = hist[0].Fail
			s.batchSize = hist[0].OK + hist[0].Fail
		}
		s.mu.Unlock()
	}
}

func (s *Service) Snapshot() Progress {
	if s == nil {
		return Progress{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hist := append([]BatchSummary(nil), s.history...)
	return Progress{
		Running: DefaultLogs.Running(),
		Size: s.batchSize, OK: s.batchOK, Fail: s.batchFail,
		LifetimeOK: s.lifeOK, LifetimeFail: s.lifeFail, LifetimeBatches: s.lifeN,
		RawTotal: s.rawTotal, RawUnchecked: s.rawUnchecked,
		Last: s.lastBatch, History: hist,
	}
}

func LiveProgress() Progress {
	liveMu.RLock()
	svc := live
	liveMu.RUnlock()
	if svc == nil {
		return Progress{}
	}
	return svc.Snapshot()
}

func (s *Service) LastBatch() BatchSummary {
	if s == nil {
		return BatchSummary{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastBatch
}

// LastSourceBatch is this-round ok/fail per source (skips like blocked-country omitted).
func (s *Service) LastSourceBatch() map[string][2]int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][2]int, len(s.sourceBatch))
	for k, v := range s.sourceBatch {
		out[k] = v
	}
	return out
}

func classifyValidateErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "blocked country"):
		return "blocked_country"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "connect") || strings.Contains(msg, "refused") || strings.Contains(msg, "no route"):
		return "connect"
	default:
		return "fail"
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

// ValidateBatch tests one slice of the raw queue (never-checked first).
// It returns how many raw addresses still have no LastCheck, so the scheduler
// can keep issuing batches until a pass completes.
func (s *Service) ValidateBatch(ctx context.Context, limit int64) int {
	if s == nil || s.free == nil {
		return 0
	}
	if DefaultLogs.Running() {
		s.mu.RLock()
		n := s.rawUnchecked
		s.mu.RUnlock()
		return n
	}
	if limit <= 0 {
		limit = 400
	}
	store := s.free.Store()

	// Untested raw first (so other sources can keep filling the 4000 cap),
	// then due retries, then a little maintenance recheck of already-live ones.
	reLimit := limit * 2 / 10
	retryLimit := limit * 2 / 10
	var scored []freproxies.Proxy
	if reLimit > 0 {
		scored, _ = store.ListValidated(ctx, reLimit)
	}
	retries, retryWaiting, _ := store.PickRetry(ctx, retryLimit)
	rawNeed := limit - int64(len(scored)+len(retries))
	if rawNeed < limit/2 {
		rawNeed = limit / 2
	}

	raw, rawTotal, rawUnchecked, err := store.PickRaw(ctx, rawNeed)
	if err != nil {
		log.Printf("validator list raw: %v", err)
		DefaultLogs.Add("fail", "", "list raw failed: "+err.Error(), "", 0)
		return rawUnchecked
	}
	s.mu.Lock()
	s.rawTotal = rawTotal
	s.rawUnchecked = rawUnchecked
	s.mu.Unlock()

	batch := append(append(raw, retries...), scored...)
	if len(batch) == 0 {
		DefaultLogs.Add("info", "", "校验批次跳过：无待验代理", "", 0)
		return 0
	}
	neverInBatch := 0
	for _, p := range raw {
		if p.LastCheck.IsZero() {
			neverInBatch++
		}
	}
	remaining := rawUnchecked - neverInBatch
	if remaining < 0 {
		remaining = 0
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

	started := time.Now()
	s.mu.Lock()
	s.batchSize = len(batch)
	s.batchOK = 0
	s.batchFail = 0
	s.sourceBatch = map[string][2]int{}
	s.mu.Unlock()
	DefaultLogs.SetRunning(true)
	DefaultLogs.Add("info", "", "开始校验 batch="+strconv.Itoa(len(batch))+
		" raw="+strconv.Itoa(len(raw))+" retry="+strconv.Itoa(len(retries))+
		" recheck="+strconv.Itoa(len(scored))+
		" unchecked="+strconv.Itoa(rawUnchecked)+"/"+strconv.Itoa(rawTotal)+
		" retry_wait="+strconv.Itoa(retryWaiting)+
		" urls="+strconv.Itoa(len(urls)), "", 0)
	defer DefaultLogs.SetRunning(false)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var okCount, failCount int
	var mu sync.Mutex
	sourceBatch := map[string][2]int{}
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
			kind := classifyValidateErr(lastErr)
			if okResult {
				okCount++
				DefaultLogs.Add("ok", p.Addr, "通过", p.Source, latency)
				sourcestats.Default.Record(p.Source, true, latency)
				pair := sourceBatch[p.Source]
				pair[0]++
				sourceBatch[p.Source] = pair
			} else if kind == "blocked_country" {
				msg := lastErr.Error()
				DefaultLogs.Add("skip", p.Addr, msg, p.Source, 0)
			} else {
				failCount++
				msg := kind
				if lastErr != nil {
					msg = lastErr.Error()
				}
				DefaultLogs.Add("fail", p.Addr, msg, p.Source, 0)
				sourcestats.Default.Record(p.Source, false, 0)
				pair := sourceBatch[p.Source]
				pair[1]++
				sourceBatch[p.Source] = pair
			}
			s.mu.Lock()
			s.batchOK = okCount
			s.batchFail = failCount
			s.mu.Unlock()
			mu.Unlock()
		}()
	}
	wg.Wait()
	sourcestats.Default.Evaluate(minSample, minRate)
	elapsed := time.Since(started)
	finished := BatchSummary{
		OK: okCount, Fail: failCount, Raw: len(raw), Recheck: len(scored),
		Duration: elapsed, At: time.Now().UTC(),
	}
	s.mu.Lock()
	s.lastBatch = finished
	s.batchOK = okCount
	s.batchFail = failCount
	s.lifeOK += int64(okCount)
	s.lifeFail += int64(failCount)
	s.lifeN++
	s.rawUnchecked = remaining
	s.sourceBatch = sourceBatch
	s.history = append([]BatchSummary{finished}, s.history...)
	if len(s.history) > 20 {
		s.history = s.history[:20]
	}
	s.mu.Unlock()
	if s.store != nil {
		_ = s.store.InsertValidateBatch(ctx, okCount, failCount, len(raw), len(scored), elapsed.Milliseconds())
	}
	sourcestats.Default.Flush()
	summary := "validate batch done ok=" + strconv.Itoa(okCount) + " fail=" + strconv.Itoa(failCount) +
		" raw=" + strconv.Itoa(len(raw)) + " recheck=" + strconv.Itoa(len(scored)) +
		" left=" + strconv.Itoa(remaining) + "/" + strconv.Itoa(rawTotal) +
		" dur=" + elapsed.Truncate(time.Millisecond).String()
	_ = store.PushEvent(ctx, summary)
	DefaultLogs.Add("info", "", summary, "", 0)
	s.free.NotifyValidateBatch(okCount, failCount)
	log.Printf("validate batch: ok=%d fail=%d (raw=%d recheck=%d left=%d/%d dur=%s)", okCount, failCount, len(raw), len(scored), remaining, rawTotal, elapsed.Truncate(time.Millisecond).String())
	if okCount == 0 && failCount > 0 {
		webhook.Default.Notify("validate_all_fail", map[string]any{"fail": failCount})
	}
	return remaining
}
