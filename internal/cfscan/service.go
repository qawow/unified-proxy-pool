package cfscan

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/db"
	"unified-proxy-pool/internal/events"
)

type Status struct {
	Running    bool   `json:"running"`
	Phase      string `json:"phase"`
	Total      int    `json:"total"`
	TCPDone    int    `json:"tcp_done"`
	TCPOpen    int    `json:"tcp_open"`
	TLSDone    int    `json:"tls_done"`
	Hits       int    `json:"hits"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type RunRequest struct {
	Targets     string `json:"targets"`
	TCPConc     int    `json:"tcp_conc"`
	TLSConc     int    `json:"tls_conc"`
	TCPTimeoutMS int   `json:"tcp_timeout_ms"`
	TLSTimeoutMS int   `json:"tls_timeout_ms"`
}

type Service struct {
	store  *db.Store
	events *events.Broker

	mu     sync.Mutex
	cancel context.CancelFunc
	st     Status
}

func New(store *db.Store, broker *events.Broker) *Service {
	return &Service{store: store, events: broker}
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *Service) Stop() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (s *Service) ListHits(ctx context.Context, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT ip, colo, fl, sni, latency_ms, last_seen
		FROM cf_scan_hits ORDER BY latency_ms ASC, last_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var seen time.Time
		if err := rows.Scan(&h.IP, &h.Colo, &h.FL, &h.SNI, &h.LatencyMS, &seen); err != nil {
			return nil, err
		}
		h.LastSeen = seen.UTC().Format(time.RFC3339Nano)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Service) ClearHits(ctx context.Context) error {
	_, err := s.store.DB.ExecContext(ctx, `DELETE FROM cf_scan_hits`)
	return err
}

func (s *Service) upsertHit(ctx context.Context, h Hit) error {
	now := time.Now().UTC()
	_, err := s.store.DB.ExecContext(ctx, `INSERT INTO cf_scan_hits(ip, colo, fl, sni, latency_ms, open_443, last_seen)
		VALUES(?,?,?,?,?,1,?)
		ON CONFLICT(ip) DO UPDATE SET colo=excluded.colo, fl=excluded.fl, sni=excluded.sni,
			latency_ms=excluded.latency_ms, last_seen=excluded.last_seen`,
		h.IP, h.Colo, h.FL, h.SNI, h.LatencyMS, now)
	return err
}

func (s *Service) Start(req RunRequest) error {
	ips, err := ParseTargets(req.Targets)
	if err != nil {
		return err
	}
	tcpConc := req.TCPConc
	if tcpConc <= 0 {
		tcpConc = 400
	}
	if tcpConc > 2000 {
		tcpConc = 2000
	}
	tlsConc := req.TLSConc
	if tlsConc <= 0 {
		tlsConc = 80
	}
	if tlsConc > 400 {
		tlsConc = 400
	}
	tcpTO := time.Duration(req.TCPTimeoutMS) * time.Millisecond
	if tcpTO <= 0 {
		tcpTO = 1500 * time.Millisecond
	}
	tlsTO := time.Duration(req.TLSTimeoutMS) * time.Millisecond
	if tlsTO <= 0 {
		tlsTO = 4000 * time.Millisecond
	}

	s.mu.Lock()
	if s.st.Running {
		s.mu.Unlock()
		return errAlreadyRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.st = Status{Running: true, Phase: "tcp", Total: len(ips), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.mu.Unlock()

	go s.run(ctx, ips, tcpConc, tlsConc, tcpTO, tlsTO)
	return nil
}

var errAlreadyRunning = errString("scan already running")

type errString string

func (e errString) Error() string { return string(e) }

func (s *Service) run(ctx context.Context, ips []string, tcpConc, tlsConc int, tcpTO, tlsTO time.Duration) {
	defer func() {
		s.mu.Lock()
		s.st.Running = false
		if s.st.Phase != "error" {
			s.st.Phase = "done"
		}
		s.st.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		s.cancel = nil
		s.mu.Unlock()
		if s.events != nil {
			s.events.Publish("cfscan.finished", s.Status())
		}
	}()

	open := s.scanTCP(ctx, ips, tcpConc, tcpTO)
	if ctx.Err() != nil {
		return
	}
	s.setPhase("tls")
	s.scanTLS(ctx, open, tlsConc, tlsTO)
}

func (s *Service) setPhase(p string) {
	s.mu.Lock()
	s.st.Phase = p
	s.mu.Unlock()
}

func (s *Service) scanTCP(ctx context.Context, ips []string, conc int, timeout time.Duration) []string {
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var openMu sync.Mutex
	open := make([]string, 0, 64)
	var done atomic.Int64
	var nopen atomic.Int64
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		ip := ip
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ok := tcpOpen(ctx, ip, 443, timeout)
			d := int(done.Add(1))
			if ok {
				n := int(nopen.Add(1))
				openMu.Lock()
				open = append(open, ip)
				openMu.Unlock()
				s.mu.Lock()
				s.st.TCPDone = d
				s.st.TCPOpen = n
				s.mu.Unlock()
			} else {
				s.mu.Lock()
				s.st.TCPDone = d
				s.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return open
}

func (s *Service) scanTLS(ctx context.Context, ips []string, conc int, handshake time.Duration) {
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var done atomic.Int64
	var hits atomic.Int64
	readTO := 5 * time.Second
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		ip := ip
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			hit := false
			var h Hit
			for _, sni := range probeSNIs {
				if ctx.Err() != nil {
					break
				}
				got, ok := tlsCFProbe(ctx, ip, 443, sni, handshake, readTO)
				if ok {
					h, hit = got, true
					break
				}
			}
			d := int(done.Add(1))
			if hit {
				n := int(hits.Add(1))
				if err := s.upsertHit(context.Background(), h); err != nil {
					log.Printf("cfscan save %s: %v", h.IP, err)
				}
				s.mu.Lock()
				s.st.TLSDone = d
				s.st.Hits = n
				s.mu.Unlock()
			} else {
				s.mu.Lock()
				s.st.TLSDone = d
				s.mu.Unlock()
			}
		}()
	}
	wg.Wait()
}
