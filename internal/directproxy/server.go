package directproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/conntrack"
	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/netutil"
	"unified-proxy-pool/internal/traffic"
)

type Config struct {
	ListenAddr   string
	Username     string
	Password     string
	Enabled      bool
	ChainEnabled bool
	ChainAddr    string
	ChainHops    int // 2 = entry→exit, 3 = entry→mid→exit
}

type Server struct {
	cfg    Config
	free   *freproxies.Service
	ln     net.Listener
	chainLn net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.RWMutex
	chainHops int

	requests atomic.Int64
	success  atomic.Int64
	failures atomic.Int64
	running  atomic.Bool

	chainRequests atomic.Int64
	chainSuccess  atomic.Int64
	chainFailures atomic.Int64
	chainRunning  atomic.Bool

	// F3/F5 runtime options
	stickyEnabled bool
	sticky        stickyStore
	forceAuth     bool
	allowedNets   []*net.IPNet
	rateLimitBps  int64

	chainOpts ChainOptions

	// channels records per-destination outcomes so a proxy can be sidelined for
	// one target site without being penalised everywhere. Optional.
	channels channelRecorder
}

// channelRecorder is the slice of chanpolicy.Registry this server needs.
type channelRecorder interface {
	ChannelFor(target string) string
	Record(o chanpolicy.Outcome) *chanpolicy.Ban
}

// SetChannelPolicy attaches the per-channel outcome recorder.
func (s *Server) SetChannelPolicy(rec channelRecorder) {
	s.mu.Lock()
	s.channels = rec
	s.mu.Unlock()
}

func (s *Server) channelRec() channelRecorder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channels
}

// channelFor derives the channel for a dial target, or "" when channel tracking
// is off.
func (s *Server) channelFor(target string) string {
	rec := s.channelRec()
	if rec == nil {
		return ""
	}
	return rec.ChannelFor(target)
}

// recordChannel files one outcome. Safe to call with an empty channel or addr.
func (s *Server) recordChannel(channel, addr string, ok bool, status int, errTag string, latencyMS int64) {
	if channel == "" || addr == "" {
		return
	}
	rec := s.channelRec()
	if rec == nil {
		return
	}
	rec.Record(chanpolicy.Outcome{
		Channel:   channel,
		Addr:      addr,
		OK:        ok,
		Status:    status,
		Err:       errTag,
		LatencyMS: latencyMS,
	})
}

// errTag reduces a dial error to a short stable tag. The full text is unbounded
// and would make ban reasons unreadable, but timeout-vs-refused has to survive
// because the two trip different rules.
func errTag(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "refused"):
		return "conn_refused"
	case strings.Contains(msg, "reset"):
		return "conn_reset"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "dns"):
		return "dns_failed"
	case strings.Contains(msg, "connect status"), strings.Contains(msg, "status"):
		return "upstream_rejected"
	default:
		return "dial_failed"
	}
}

type stickyStore interface {
	Get(clientIP string) (string, bool)
	Put(clientIP, addr string)
	GetProxy(clientIP string) (addr, protocol string, ok bool)
	PutProxy(clientIP, addr, protocol string)
}

// ChainOptions is runtime multi-hop policy (from feature.chain).
type ChainOptions struct {
	Enabled              bool     `json:"enabled"`
	ListenAddr           string   `json:"listen_addr,omitempty"`
	Hops                 int      `json:"hops"`
	FailoverTries        int      `json:"failover_tries"`
	DialTimeoutMS        int      `json:"dial_timeout_ms"`
	HopTimeoutMS         int      `json:"hop_timeout_ms"`
	PreferDistinctHost   bool     `json:"prefer_distinct_host"`
	PreferDistinctRegion bool     `json:"prefer_distinct_region"`
	EntryProto           string   `json:"entry_proto,omitempty"`
	ExitProto            string   `json:"exit_proto,omitempty"`
	EntryRegion          string   `json:"entry_region,omitempty"`
	ExitRegion           string   `json:"exit_region,omitempty"`
	StickyEnabled        bool     `json:"sticky_enabled"`
	StickyTTLSec         int      `json:"sticky_ttl_sec"`
	AuthRequired         bool     `json:"auth_required"`
	Username             string   `json:"username,omitempty"`
	Password             string   `json:"password,omitempty"`
	AllowedCIDRs         []string `json:"allowed_cidrs,omitempty"`
	RateLimitBPS         int64    `json:"rate_limit_bps"`
	MaxParallelDial      int      `json:"max_parallel_dial"`
}

func DefaultChainOptions() ChainOptions {
	return ChainOptions{
		Enabled:            true,
		ListenAddr:         "0.0.0.0:7893",
		Hops:               2,
		FailoverTries:      6,
		DialTimeoutMS:      8000,
		HopTimeoutMS:       5000,
		PreferDistinctHost: true,
		MaxParallelDial:    1,
		StickyTTLSec:       600,
	}
}

type Status struct {
	Enabled        bool              `json:"enabled"`
	Running        bool              `json:"running"`
	ListenAddr     string            `json:"listen_addr"`
	LANIPs         []string          `json:"lan_ips"`
	ClientHost     string            `json:"client_host"`
	ClientHTTP     string            `json:"client_http"`
	ClientSOCKS5   string            `json:"client_socks5"`
	ClientExamples map[string]string `json:"client_examples"`
	Username       string            `json:"username,omitempty"`
	Requests       int64             `json:"requests"`
	Success        int64             `json:"success"`
	Failures       int64             `json:"failures"`

	// 代理套代理（多跳）
	ChainEnabled    bool              `json:"chain_enabled"`
	ChainRunning    bool              `json:"chain_running"`
	ChainListenAddr string            `json:"chain_listen_addr"`
	ChainHops       int               `json:"chain_hops"`
	ChainHTTP       string            `json:"chain_http"`
	ChainSOCKS5     string            `json:"chain_socks5"`
	ChainExamples   map[string]string `json:"chain_examples"`
	ChainRequests   int64             `json:"chain_requests"`
	ChainSuccess    int64             `json:"chain_success"`
	ChainFailures   int64             `json:"chain_failures"`
	ChainDesc       string            `json:"chain_desc"`
	ChainPath       string            `json:"chain_path"`
	ChainLabel      string            `json:"chain_label"` // 展示名：链式代理
	ChainOptions    ChainOptions      `json:"chain_options"`
}

func New(cfg Config, free *freproxies.Service) *Server {
	hops := cfg.ChainHops
	if hops < 2 {
		hops = 2
	}
	if hops > 4 {
		hops = 4
	}
	opts := DefaultChainOptions()
	opts.Enabled = cfg.ChainEnabled
	opts.ListenAddr = cfg.ChainAddr
	if opts.ListenAddr == "" {
		opts.ListenAddr = "0.0.0.0:7893"
	}
	opts.Hops = hops
	return &Server{cfg: cfg, free: free, chainHops: hops, chainOpts: opts}
}

func (s *Server) SetChainOptions(opts ChainOptions) {
	if opts.Hops < 2 {
		opts.Hops = 2
	}
	if opts.Hops > 4 {
		opts.Hops = 4
	}
	if opts.FailoverTries <= 0 {
		opts.FailoverTries = 6
	}
	if opts.DialTimeoutMS <= 0 {
		opts.DialTimeoutMS = 8000
	}
	if opts.HopTimeoutMS <= 0 {
		opts.HopTimeoutMS = 5000
	}
	if opts.MaxParallelDial < 1 {
		opts.MaxParallelDial = 1
	}
	s.mu.Lock()
	s.chainOpts = opts
	s.chainHops = opts.Hops
	if opts.ListenAddr != "" {
		s.cfg.ChainAddr = opts.ListenAddr
	}
	s.cfg.ChainEnabled = opts.Enabled
	s.mu.Unlock()
}

func (s *Server) GetChainOptions() ChainOptions {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chainOpts
}

func (s *Server) SetChainHops(n int) {
	if n < 2 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	s.mu.Lock()
	s.chainHops = n
	s.mu.Unlock()
}

func (s *Server) ChainHops() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.chainHops < 2 {
		return 2
	}
	return s.chainHops
}

func (s *Server) SetSticky(st stickyStore, enabled bool) {
	s.mu.Lock()
	s.sticky = st
	s.stickyEnabled = enabled
	s.mu.Unlock()
}

func (s *Server) SetAuthRequired(v bool) {
	s.mu.Lock()
	s.forceAuth = v
	s.mu.Unlock()
}

func (s *Server) SetRateLimit(bps int64) {
	s.mu.Lock()
	s.rateLimitBps = bps
	s.mu.Unlock()
}

func (s *Server) SetAllowedCIDRs(cidrs []string) {
	var nets []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			c = c + "/32"
		}
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			nets = append(nets, n)
		}
	}
	s.mu.Lock()
	s.allowedNets = nets
	s.mu.Unlock()
}

func (s *Server) allowIP(ip net.IP) bool {
	s.mu.RLock()
	nets := s.allowedNets
	s.mu.RUnlock()
	if len(nets) == 0 {
		return true
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) Status() Status {
	listen := s.cfg.ListenAddr
	if listen == "" {
		listen = "0.0.0.0:7892"
	}
	chainListen := s.cfg.ChainAddr
	if chainListen == "" {
		chainListen = "0.0.0.0:7893"
	}
	lanIPs := netutil.LANIPs()
	endpoints := netutil.ClientEndpoints(listen)
	chainEP := netutil.ClientEndpoints(chainListen)
	hops := s.ChainHops()
	examples := map[string]string{
		"curl":      "curl -x " + endpoints["http"] + " https://httpbin.org/ip",
		"export":    "export http_proxy=" + endpoints["http"] + " https_proxy=" + endpoints["http"] + " ALL_PROXY=" + endpoints["socks5"],
		"git":       "git config --global http.proxy " + endpoints["http"],
		"windows":   "set http_proxy=" + endpoints["http"] + " && set https_proxy=" + endpoints["http"],
		"clash_url": endpoints["http"],
	}
	path := ChainPathLabel(hops)
	chainExamples := map[string]string{
		"curl":   "curl -x " + chainEP["http"] + " https://httpbin.org/ip",
		"export": "export http_proxy=" + chainEP["http"] + " https_proxy=" + chainEP["http"] + " ALL_PROXY=" + chainEP["socks5"],
		"desc":   "流量路径: " + path,
		"path":   path,
	}
	if s.cfg.Username != "" {
		authURL := "http://" + s.cfg.Username + ":***@" + endpoints["host"]
		examples["curl_auth"] = "curl -x " + authURL + " https://httpbin.org/ip"
		chainAuth := "http://" + s.cfg.Username + ":***@" + chainEP["host"]
		chainExamples["curl_auth"] = "curl -x " + chainAuth + " https://httpbin.org/ip"
	}
	return Status{
		Enabled:        s.cfg.Enabled,
		Running:        s.running.Load(),
		ListenAddr:     listen,
		LANIPs:         lanIPs,
		ClientHost:     endpoints["host"],
		ClientHTTP:     endpoints["http"],
		ClientSOCKS5:   endpoints["socks5"],
		ClientExamples: examples,
		Username:       s.cfg.Username,
		Requests:       s.requests.Load(),
		Success:        s.success.Load(),
		Failures:       s.failures.Load(),

		ChainEnabled:    s.cfg.ChainEnabled,
		ChainRunning:    s.chainRunning.Load(),
		ChainListenAddr: chainListen,
		ChainHops:       hops,
		ChainHTTP:       chainEP["http"],
		ChainSOCKS5:     chainEP["socks5"],
		ChainExamples:   chainExamples,
		ChainRequests:   s.chainRequests.Load(),
		ChainSuccess:    s.chainSuccess.Load(),
		ChainFailures:   s.chainFailures.Load(),
		ChainDesc:       fmt.Sprintf("链式代理：%d 跳 · %s", hops, ChainPathLabel(hops)),
		ChainPath:       ChainPathLabel(hops),
		ChainLabel:      "链式代理",
		ChainOptions:    s.GetChainOptions(),
	}
}

// ChainPathLabel builds 本机 → 入口 → … → 出口 → 目标
func ChainPathLabel(hops int) string {
	if hops < 2 {
		hops = 2
	}
	parts := []string{"本机", "入口"}
	for i := 0; i < hops-2; i++ {
		parts = append(parts, "中继")
	}
	parts = append(parts, "出口", "目标")
	return strings.Join(parts, " → ")
}

func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.free == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	if s.cfg.Enabled {
		if s.cfg.ListenAddr == "" {
			s.cfg.ListenAddr = "0.0.0.0:7892"
		}
		ln, err := net.Listen("tcp", s.cfg.ListenAddr)
		if err != nil {
			cancel()
			return err
		}
		s.ln = ln
		s.running.Store(true)
		s.wg.Add(1)
		go s.serveLoop(runCtx, ln, false)
		log.Printf("directproxy single-hop listening on %s", s.cfg.ListenAddr)
	}

	if s.cfg.ChainEnabled {
		if s.cfg.ChainAddr == "" {
			s.cfg.ChainAddr = "0.0.0.0:7893"
		}
		cln, err := net.Listen("tcp", s.cfg.ChainAddr)
		if err != nil {
			log.Printf("directproxy chain listen skipped: %v", err)
		} else {
			s.chainLn = cln
			s.chainRunning.Store(true)
			s.wg.Add(1)
			go s.serveLoop(runCtx, cln, true)
			log.Printf("directproxy chain (%d-hop) listening on %s", s.ChainHops(), s.cfg.ChainAddr)
		}
	}

	go func() {
		<-runCtx.Done()
		if s.ln != nil {
			_ = s.ln.Close()
		}
		if s.chainLn != nil {
			_ = s.chainLn.Close()
		}
	}()
	return nil
}

func (s *Server) serveLoop(ctx context.Context, ln net.Listener, chain bool) {
	defer s.wg.Done()
	if chain {
		defer s.chainRunning.Store(false)
	} else {
		defer s.running.Store(false)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				if !errors.Is(err, net.ErrClosed) {
					log.Printf("directproxy accept: %v", err)
				}
				return
			}
		}
		// CIDR allow list
		if ra, ok := conn.RemoteAddr().(*net.TCPAddr); ok && !s.allowIP(ra.IP) {
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handle(ctx, c, chain)
		}(conn)
	}
}

func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	if s.chainLn != nil {
		_ = s.chainLn.Close()
	}
	s.wg.Wait()
	s.running.Store(false)
	s.chainRunning.Store(false)
}

type ctxKey int

const (
	ctxClientIP ctxKey = iota
	ctxTrackID
)

func withClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxClientIP, ip)
}

func clientIPFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxClientIP).(string)
	return s
}

func remoteIP(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

func (s *Server) handle(ctx context.Context, conn net.Conn, chain bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	ip := remoteIP(conn)
	ctx = withClientIP(ctx, ip)
	br := bufio.NewReader(conn)
	peek, err := br.Peek(1)
	if err != nil {
		return
	}
	ch := "single"
	if chain {
		ch = "chain"
		s.chainRequests.Add(1)
	} else {
		s.requests.Add(1)
	}
	// Live-connection registry. Begin/End were never called, so the dashboard
	// card and upp_active_connections always reported 0.
	trackID := conntrack.Default.Begin(ch, ip)
	ctx = context.WithValue(ctx, ctxTrackID, trackID)
	defer conntrack.Default.End(trackID, 0, 0)
	// 入站：客户端连入本机监听端口；未进入 relay 时由 defer 释放
	traffic.Default.BeginInbound(ch)
	finished := false
	defer func() {
		if !finished {
			traffic.Default.EndConn(ch, false, 0, 0, false)
		}
	}()

	var handleErr error
	if peek[0] == 0x05 {
		handleErr = s.handleSOCKS5(ctx, conn, br, chain, &finished)
	} else {
		handleErr = s.handleHTTP(ctx, conn, br, chain, &finished)
	}
	if handleErr != nil {
		if chain {
			s.chainFailures.Add(1)
		} else {
			s.failures.Add(1)
		}
		return
	}
	if chain {
		s.chainSuccess.Add(1)
	} else {
		s.success.Add(1)
	}
}

func (s *Server) authRequired() bool {
	s.mu.RLock()
	force := s.forceAuth
	s.mu.RUnlock()
	if force {
		return true
	}
	return s.cfg.Username != "" || s.cfg.Password != ""
}

func (s *Server) checkUserPass(user, pass string) bool {
	if !s.authRequired() {
		return true
	}
	return user == s.cfg.Username && pass == s.cfg.Password
}

func (s *Server) pickUpstream(ctx context.Context) (freproxies.Proxy, error) {
	return s.free.PickValidated(ctx, "")
}

func (s *Server) dialVia(ctx context.Context, upstream freproxies.Proxy, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	proxyAddr := upstream.Addr
	proto := strings.ToLower(upstream.Protocol)

	if proto == "socks5" || proto == "socks4" || proto == "socks" {
		return dialSOCKS5Via(ctx, dialer, proxyAddr, target)
	}
	// default HTTP CONNECT upstream
	return dialHTTPConnectVia(ctx, dialer, proxyAddr, target)
}

// dialViaWithFailover tries several free proxies until one connects.
func (s *Server) dialViaWithFailover(ctx context.Context, target string) (net.Conn, freproxies.Proxy, error) {
	return s.dialViaWithFailoverClient(ctx, target, clientIPFrom(ctx))
}

func (s *Server) dialViaWithFailoverClient(ctx context.Context, target, clientIP string) (net.Conn, freproxies.Proxy, error) {
	s.mu.RLock()
	stickyOn := s.stickyEnabled && s.sticky != nil
	sticky := s.sticky
	s.mu.RUnlock()
	channel := s.channelFor(target)
	if stickyOn && clientIP != "" {
		if addr, proto, ok := sticky.GetProxy(clientIP); ok {
			if proto == "" {
				proto = "http"
			}
			up := freproxies.Proxy{Addr: addr, Protocol: proto}
			// A sticky proxy that the destination has since sidelined must not be
			// reused, or stickiness would quietly defeat the ban.
			if !s.channelBanned(channel, addr) {
				start := time.Now()
				if conn, err := dialProxyChain(ctx, []freproxies.Proxy{up}, target); err == nil {
					s.recordChannel(channel, addr, true, 0, "", time.Since(start).Milliseconds())
					return conn, up, nil
				}
			}
		}
	}
	res, err := s.free.Pick(ctx, freproxies.PickOptions{N: 8, Channel: channel})
	if err != nil {
		return nil, freproxies.Proxy{}, err
	}
	upstreams := res.Items
	var lastErr error
	for _, up := range upstreams {
		start := time.Now()
		conn, err := dialProxyChain(ctx, []freproxies.Proxy{up}, target)
		if err == nil {
			if stickyOn && clientIP != "" {
				sticky.PutProxy(clientIP, up.Addr, up.Protocol)
			}
			// A successful dial only proves the tunnel opened. For HTTPS that is all
			// this layer will ever know; the application-layer verdict, if any,
			// arrives later via the report API.
			s.recordChannel(channel, up.Addr, true, 0, "", time.Since(start).Milliseconds())
			return conn, up, nil
		}
		lastErr = err
		_ = s.free.Store().MarkValidated(ctx, up.Addr, 0, false)
		// Global score already took the hit above; this records that the failure
		// happened against *this* destination, which is what scopes the ban.
		s.recordChannel(channel, up.Addr, false, 0, errTag(err), 0)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all upstreams failed")
	}
	return nil, freproxies.Proxy{}, lastErr
}

// channelBanned reports whether a specific addr is sidelined for a channel.
func (s *Server) channelBanned(channel, addr string) bool {
	if channel == "" || addr == "" {
		return false
	}
	rec := s.channelRec()
	if rec == nil {
		return false
	}
	type banChecker interface {
		Banned(channel, addr string) bool
	}
	if bc, ok := rec.(banChecker); ok {
		return bc.Banned(channel, addr)
	}
	return false
}

// dialChainWithFailover: multi-hop 链式代理.
func (s *Server) dialChainWithFailover(ctx context.Context, target string) (net.Conn, []freproxies.Proxy, error) {
	opts := s.GetChainOptions()
	hopsN := opts.Hops
	if hopsN < 2 {
		hopsN = s.ChainHops()
	}
	tries := opts.FailoverTries
	if tries <= 0 {
		tries = 6
	}
	dialTO := time.Duration(opts.DialTimeoutMS) * time.Millisecond
	if dialTO <= 0 {
		dialTO = 8 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTO)
	defer cancel()

	channel := s.channelFor(target)

	var pool []freproxies.Proxy
	if s.free != nil && s.free.Hot() != nil {
		pool = s.free.Hot().PickDistinct(hopsN*8, opts.PreferDistinctRegion, opts.EntryProto, opts.ExitProto, opts.EntryRegion, opts.ExitRegion)
	}
	if len(pool) < hopsN {
		more, err := s.free.Pick(dialCtx, freproxies.PickOptions{N: hopsN * 8, Channel: channel})
		if err == nil {
			pool = append(pool, more.Items...)
		}
	}
	if len(pool) == 0 {
		return nil, nil, fmt.Errorf("no free proxy for chain")
	}
	if len(pool) < hopsN {
		hopsN = len(pool)
	}

	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		start := (attempt * hopsN) % len(pool)
		rotated := append(append([]freproxies.Proxy{}, pool[start:]...), pool[:start]...)
		var hops []freproxies.Proxy
		if opts.PreferDistinctHost {
			hops = uniqueHops(rotated, hopsN)
		} else {
			if len(rotated) > hopsN {
				hops = rotated[:hopsN]
			} else {
				hops = rotated
			}
		}
		if len(hops) < 1 {
			continue
		}
		// apply entry/exit proto soft preference by swap if possible
		hops = applyEntryExitPrefs(hops, opts)
		// Only the exit hop is visible to the destination, so only it is subject to
		// that destination's bans. Filtering every hop would starve the chain over
		// relay proxies the target never sees.
		hops = s.avoidBannedExit(hops, rotated, channel)
		conn, err := dialProxyChain(dialCtx, hops, target)
		if err == nil {
			return conn, hops, nil
		}
		lastErr = err
		_ = s.free.Store().MarkValidated(dialCtx, hops[0].Addr, 0, false)
		if s.free.Hot() != nil {
			s.free.Hot().Invalidate(hops[0].Addr)
		}
		// Deliberately not recorded against the channel: a chain that failed to
		// build never reached the destination, so there is nothing to attribute to
		// the exit proxy. Only the entry hop is known to be at fault, and that is
		// already reflected in its global score above.
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all chains failed")
	}
	return nil, nil, lastErr
}

// avoidBannedExit swaps the exit hop for one the destination has not sidelined.
//
// It searches the wider candidate pool rather than only the chosen hops, and
// leaves the chain untouched when no clean substitute exists — a chain with a
// banned exit still beats no chain at all, and the Relaxed path in selection
// makes the same trade.
func (s *Server) avoidBannedExit(hops, pool []freproxies.Proxy, channel string) []freproxies.Proxy {
	if channel == "" || len(hops) == 0 {
		return hops
	}
	last := len(hops) - 1
	if !s.channelBanned(channel, hops[last].Addr) {
		return hops
	}
	inChain := make(map[string]bool, len(hops))
	for _, h := range hops {
		inChain[h.Addr] = true
	}
	for _, cand := range pool {
		if inChain[cand.Addr] || s.channelBanned(channel, cand.Addr) {
			continue
		}
		out := append([]freproxies.Proxy(nil), hops...)
		out[last] = cand
		return out
	}
	return hops
}

func applyEntryExitPrefs(hops []freproxies.Proxy, opts ChainOptions) []freproxies.Proxy {
	if len(hops) == 0 {
		return hops
	}
	out := append([]freproxies.Proxy{}, hops...)
	if opts.EntryProto != "" {
		for i := range out {
			if strings.EqualFold(out[i].Protocol, opts.EntryProto) {
				out[0], out[i] = out[i], out[0]
				break
			}
		}
	}
	if opts.ExitProto != "" && len(out) > 1 {
		last := len(out) - 1
		for i := range out {
			if strings.EqualFold(out[i].Protocol, opts.ExitProto) {
				out[last], out[i] = out[i], out[last]
				break
			}
		}
	}
	return out
}

// openUpstream dials the target and reports which proxy the destination will
// actually see — the exit hop for a chain, the only hop otherwise.
//
// The caller needs that identity to attribute an application-layer verdict (a
// plain-HTTP status code) to the right proxy. Everything below the tunnel is
// invisible to us, so this is the only attribution the pool can make on its own.
func (s *Server) openUpstream(ctx context.Context, target string, chain bool) (net.Conn, freproxies.Proxy, error) {
	var (
		conn net.Conn
		exit freproxies.Proxy
		err  error
	)
	if chain {
		var hops []freproxies.Proxy
		conn, hops, err = s.dialChainWithFailover(ctx, target)
		if len(hops) > 0 {
			exit = hops[len(hops)-1]
		}
	} else {
		conn, exit, err = s.dialViaWithFailover(ctx, target)
	}
	if err == nil && exit.Addr != "" {
		if id, ok := ctx.Value(ctxTrackID).(int64); ok && id > 0 {
			conntrack.Default.SetUpstream(id, exit.Addr)
		}
	}
	return conn, exit, err
}

func dialHTTPConnectVia(ctx context.Context, dialer *net.Dialer, proxyAddr, target string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", target, target)
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("upstream CONNECT status %d", resp.StatusCode)
	}
	// if br buffered extra, wrap
	if br.Buffered() > 0 {
		return &prefixConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func dialSOCKS5Via(ctx context.Context, dialer *net.Dialer, proxyAddr, target string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	// greeting: ver=5, nmethods=1, method=0 (no auth)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 auth rejected")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var req []byte
	req = append(req, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	req = append(req, []byte(host)...)
	var portNum int
	fmt.Sscanf(port, "%d", &portNum)
	req = append(req, byte(portNum>>8), byte(portNum))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		conn.Close()
		return nil, err
	}
	if hdr[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks5 connect failed code=%d", hdr[1])
	}
	switch hdr[3] {
	case 0x01:
		_, _ = io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			conn.Close()
			return nil, err
		}
		_, _ = io.ReadFull(conn, make([]byte, int(l[0])+2))
	case 0x04:
		_, _ = io.ReadFull(conn, make([]byte, 16+2))
	}
	return conn, nil
}

type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (s *Server) handleHTTP(ctx context.Context, client net.Conn, br *bufio.Reader, chain bool, finished *bool) error {
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	if s.authRequired() {
		user, pass, ok := parseBasicProxyAuth(req.Header.Get("Proxy-Authorization"))
		if !ok || !s.checkUserPass(user, pass) {
			_, _ = io.WriteString(client, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"upp\"\r\nContent-Length: 0\r\n\r\n")
			return fmt.Errorf("auth required")
		}
	}

	ch := "single"
	if chain {
		ch = "chain"
	}

	if req.Method == http.MethodConnect {
		target := req.Host
		if !strings.Contains(target, ":") {
			target += ":443"
		}
		up, _, err := s.openUpstream(ctx, target, chain)
		if err != nil {
			_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return err
		}
		defer up.Close()
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = client.SetDeadline(time.Time{})
		_ = up.SetDeadline(time.Time{})
		// From here the payload is an opaque TLS tunnel: no status code is
		// observable, so the dial result recorded during openUpstream is the only
		// automatic signal for this request. Application-layer verdicts (403, 429,
		// captcha) have to arrive through the report API.
		return relayTraffic(client, up, ch, finished)
	}

	// absolute-form HTTP proxy request
	targetURL := req.URL
	if !targetURL.IsAbs() {
		_, _ = io.WriteString(client, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return fmt.Errorf("non-absolute url")
	}
	host := targetURL.Host
	if !strings.Contains(host, ":") {
		if targetURL.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	upConn, exit, err := s.openUpstream(ctx, host, chain)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return err
	}
	defer upConn.Close()

	// 上游已建立：记出站；请求/响应后成对释放
	if finished != nil {
		*finished = true
	}
	traffic.Default.BeginOutbound(ch)

	// Plain HTTP is the one path where the response is readable, so it is the only
	// place the pool can see a 403/429 for itself. Attribute it to the exit proxy,
	// which is the address the destination actually saw.
	channel := s.channelFor(host)
	started := time.Now()

	outReq := req.Clone(ctx)
	outReq.RequestURI = ""
	outReq.URL = &url.URL{Scheme: targetURL.Scheme, Opaque: targetURL.Opaque, Host: targetURL.Host, Path: targetURL.Path, RawPath: targetURL.RawPath, RawQuery: targetURL.RawQuery}
	outReq.URL.Scheme = ""
	outReq.URL.Host = ""
	outReq.Header.Del("Proxy-Authorization")
	outReq.Header.Del("Proxy-Connection")
	if err := outReq.Write(upConn); err != nil {
		traffic.Default.EndConn(ch, false, 0, 0, true)
		s.recordChannel(channel, exit.Addr, false, 0, errTag(err), 0)
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(upConn), outReq)
	if err != nil {
		traffic.Default.EndConn(ch, false, 0, 0, true)
		s.recordChannel(channel, exit.Addr, false, 0, errTag(err), 0)
		return err
	}
	defer resp.Body.Close()
	// 4xx/5xx counts as a failure for this destination even though the transport
	// worked: a 403 means the site rejected this exit IP, which is exactly what a
	// per-channel ban is for. The status is passed through so status-specific
	// rules (403, 429) can fire.
	statusOK := resp.StatusCode < 400
	s.recordChannel(channel, exit.Addr, statusOK, resp.StatusCode, "", time.Since(started).Milliseconds())
	if err := resp.Write(client); err != nil {
		traffic.Default.EndConn(ch, false, 0, 0, true)
		return err
	}
	traffic.Default.EndConn(ch, true, 0, 0, true)
	return nil
}

func parseBasicProxyAuth(h string) (string, string, bool) {
	if h == "" {
		return "", "", false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(h, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Server) handleSOCKS5(ctx context.Context, client net.Conn, br *bufio.Reader, chain bool, finished *bool) error {
	// methods
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x05 {
		return fmt.Errorf("not socks5")
	}
	nMethods := int(hdr[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		return err
	}
	useUserPass := s.authRequired()
	if useUserPass {
		// method 0x02
		if _, err := client.Write([]byte{0x05, 0x02}); err != nil {
			return err
		}
		// user/pass subnegotiation
		authVer := make([]byte, 2)
		if _, err := io.ReadFull(br, authVer); err != nil {
			return err
		}
		ulen := int(authVer[1])
		user := make([]byte, ulen)
		if _, err := io.ReadFull(br, user); err != nil {
			return err
		}
		plenBuf := make([]byte, 1)
		if _, err := io.ReadFull(br, plenBuf); err != nil {
			return err
		}
		pass := make([]byte, int(plenBuf[0]))
		if _, err := io.ReadFull(br, pass); err != nil {
			return err
		}
		if !s.checkUserPass(string(user), string(pass)) {
			_, _ = client.Write([]byte{0x01, 0x01})
			return fmt.Errorf("bad credentials")
		}
		if _, err := client.Write([]byte{0x01, 0x00}); err != nil {
			return err
		}
	} else {
		if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
			return err
		}
	}

	// request
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return err
	}
	if req[0] != 0x05 || req[1] != 0x01 {
		return fmt.Errorf("unsupported socks cmd")
	}
	var host string
	switch req[3] {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(br, l); err != nil {
			return err
		}
		name := make([]byte, int(l[0]))
		if _, err := io.ReadFull(br, name); err != nil {
			return err
		}
		host = string(name)
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(br, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	default:
		return fmt.Errorf("bad atyp")
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return err
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])
	target := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	// SOCKS5 carries opaque bytes, so like CONNECT the dial result is the only
	// signal available here; channel attribution happened inside openUpstream.
	up, _, err := s.openUpstream(ctx, target, chain)
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer up.Close()
	// success
	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	_ = client.SetDeadline(time.Time{})
	_ = up.SetDeadline(time.Time{})
	ch := "single"
	if chain {
		ch = "chain"
	}
	return relayTraffic(client, up, ch, finished)
}

func relay(a, b net.Conn) error {
	return relayTraffic(a, b, "single", nil)
}

func relayTraffic(client, upstream net.Conn, channel string, finished *bool) error {
	// 入站已在 handle 中 BeginInbound；此处记出站并在结束后成对释放
	if finished != nil {
		*finished = true
	}
	traffic.Default.BeginOutbound(channel)
	up, down, err := traffic.BidirectionalRelay(client, upstream)
	ok := err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
	traffic.Default.EndConn(channel, ok, up, down, true)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
