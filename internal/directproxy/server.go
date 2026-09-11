package directproxy

import (
	"bufio"
	"context"
	"crypto/subtle"
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
	cfg     Config
	free    *freproxies.Service
	ln      net.Listener
	chainLn net.Listener
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu        sync.RWMutex
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
	chainNets     []*net.IPNet
	rateLimitBps  int64

	chainOpts ChainOptions
	viaPool   *viaPool

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
	// ExitVia is a fixed VPS hop: socks5://user:pass@host:1080 or http://host:port.
	// Mode exit (default) = last hop, destination sees the VPS IP.
	// Mode entry = first hop, destination still sees the free-proxy IP.
	ExitVia     string `json:"exit_via,omitempty"`
	ExitViaMode string `json:"exit_via_mode,omitempty"`
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
	s.chainNets = parseCIDRList(opts.AllowedCIDRs)
	if opts.ListenAddr != "" {
		s.cfg.ChainAddr = opts.ListenAddr
	}
	s.cfg.ChainEnabled = opts.Enabled
	s.mu.Unlock()
	s.rebuildViaPool()
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

// throttleFor builds the per-connection token bucket for one listener. The
// chain listener may set its own cap; otherwise the global one applies.
func (s *Server) throttleFor(chain bool) *throttle {
	s.mu.RLock()
	bps := s.rateLimitBps
	if chain && s.chainOpts.RateLimitBPS > 0 {
		bps = s.chainOpts.RateLimitBPS
	}
	s.mu.RUnlock()
	return newThrottle(bps)
}

func (s *Server) SetAllowedCIDRs(cidrs []string) {
	nets := parseCIDRList(cidrs)
	s.mu.Lock()
	s.allowedNets = nets
	s.mu.Unlock()
}

func parseCIDRList(cidrs []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

// privateNets is the implicit allow list for an unauthenticated listener.
// Both proxies bind 0.0.0.0 by default, so without this an unconfigured
// deployment on a routable host is an open internet forwarding proxy.
var privateNets = func() []*net.IPNet {
	return parseCIDRList([]string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "fc00::/7", "fe80::/10",
	})
}()

// allowIP applies the allow list for the listener the connection arrived on.
// The chain listener has its own list: an operator who restricts :7893 should
// not have to also restrict :7892, and vice versa.
//
// With no explicit list the listener is limited to private ranges unless
// credentials are configured — "no allow list" must not mean "open to the
// internet".
func (s *Server) allowIP(ip net.IP, chain bool) bool {
	s.mu.RLock()
	nets := s.allowedNets
	if chain {
		nets = s.chainNets
	}
	s.mu.RUnlock()
	if len(nets) == 0 {
		if s.authRequired(chain) {
			return true
		}
		nets = privateNets
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) Status() Status {
	// SetChainOptions mutates cfg under s.mu, so Status must not read it raw:
	// a settings update concurrent with a status poll is a data race.
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	listen := cfg.ListenAddr
	if listen == "" {
		listen = "0.0.0.0:7892"
	}
	chainListen := cfg.ChainAddr
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
	path := s.chainPathWithVia(hops)
	chainExamples := map[string]string{
		"curl":   "curl -x " + chainEP["http"] + " https://httpbin.org/ip",
		"export": "export http_proxy=" + chainEP["http"] + " https_proxy=" + chainEP["http"] + " ALL_PROXY=" + chainEP["socks5"],
		"desc":   "流量路径: " + path,
		"path":   path,
	}
	if cfg.Username != "" {
		authURL := "http://" + cfg.Username + ":***@" + endpoints["host"]
		examples["curl_auth"] = "curl -x " + authURL + " https://httpbin.org/ip"
		chainAuth := "http://" + cfg.Username + ":***@" + chainEP["host"]
		chainExamples["curl_auth"] = "curl -x " + chainAuth + " https://httpbin.org/ip"
	}
	return Status{
		Enabled:        cfg.Enabled,
		Running:        s.running.Load(),
		ListenAddr:     listen,
		LANIPs:         lanIPs,
		ClientHost:     endpoints["host"],
		ClientHTTP:     endpoints["http"],
		ClientSOCKS5:   endpoints["socks5"],
		ClientExamples: examples,
		Username:       cfg.Username,
		Requests:       s.requests.Load(),
		Success:        s.success.Load(),
		Failures:       s.failures.Load(),

		ChainEnabled:    cfg.ChainEnabled,
		ChainRunning:    s.chainRunning.Load(),
		ChainListenAddr: chainListen,
		ChainHops:       hops,
		ChainHTTP:       chainEP["http"],
		ChainSOCKS5:     chainEP["socks5"],
		ChainExamples:   chainExamples,
		ChainRequests:   s.chainRequests.Load(),
		ChainSuccess:    s.chainSuccess.Load(),
		ChainFailures:   s.chainFailures.Load(),
		ChainDesc:       fmt.Sprintf("链式代理：%d 跳 · %s", hops, path),
		ChainPath:       path,
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

// warnIfOpen makes an unauthenticated wildcard bind visible in the log. The
// connection filter already limits such a listener to private ranges, but an
// operator who meant to expose it needs to know credentials are missing.
func (s *Server) warnIfOpen(addr string, chain bool) {
	if s.authRequired(chain) {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return
	}
	name := "single-hop"
	if chain {
		name = "chain"
	}
	log.Printf("directproxy %s: %s has no credentials; access limited to LAN/private ranges. "+
		"Set a username/password (or allowed_cidrs) before exposing it.", name, addr)
}

func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.free == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.mu.Lock()
	if s.cfg.ListenAddr == "" {
		s.cfg.ListenAddr = "0.0.0.0:7892"
	}
	if s.cfg.ChainAddr == "" {
		s.cfg.ChainAddr = "0.0.0.0:7893"
	}
	cfg := s.cfg
	s.mu.Unlock()

	if cfg.Enabled {
		ln, err := net.Listen("tcp", cfg.ListenAddr)
		if err != nil {
			cancel()
			return err
		}
		s.ln = ln
		s.running.Store(true)
		s.wg.Add(1)
		go s.serveLoop(runCtx, ln, false)
		log.Printf("directproxy single-hop listening on %s", cfg.ListenAddr)
		s.warnIfOpen(cfg.ListenAddr, false)
	}

	if cfg.ChainEnabled {
		cln, err := net.Listen("tcp", cfg.ChainAddr)
		if err != nil {
			log.Printf("directproxy chain listen skipped: %v", err)
		} else {
			s.chainLn = cln
			s.chainRunning.Store(true)
			s.wg.Add(1)
			go s.serveLoop(runCtx, cln, true)
			log.Printf("directproxy chain (%d-hop) listening on %s", s.ChainHops(), cfg.ChainAddr)
			s.warnIfOpen(cfg.ChainAddr, true)
		}
	}

	s.rebuildViaPool()
	go func() {
		<-runCtx.Done()
		if s.ln != nil {
			_ = s.ln.Close()
		}
		if s.chainLn != nil {
			_ = s.chainLn.Close()
		}
		if p := s.getViaPool(); p != nil {
			p.Close()
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
	var acceptDelay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// A transient error (EMFILE under load, ECONNABORTED) used to kill
			// the listener for the rest of the process's life. Back off and keep
			// serving, the way net/http.Server does.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if acceptDelay == 0 {
				acceptDelay = 5 * time.Millisecond
			} else {
				acceptDelay *= 2
			}
			if acceptDelay > time.Second {
				acceptDelay = time.Second
			}
			log.Printf("directproxy accept: %v (retry in %v)", err, acceptDelay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(acceptDelay):
			}
			continue
		}
		acceptDelay = 0
		// CIDR allow list
		if ra, ok := conn.RemoteAddr().(*net.TCPAddr); ok && !s.allowIP(ra.IP, chain) {
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

// credsFor returns the credentials enforced on one listener. The chain listener
// has its own AuthRequired/Username/Password in ChainOptions; those used to be
// collected by the panel and then ignored, leaving :7893 wide open while the UI
// claimed otherwise.
func (s *Server) credsFor(chain bool) (required bool, user, pass string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if chain {
		o := s.chainOpts
		if o.AuthRequired || o.Username != "" || o.Password != "" {
			return true, o.Username, o.Password
		}
	}
	if s.forceAuth {
		return true, s.cfg.Username, s.cfg.Password
	}
	if s.cfg.Username != "" || s.cfg.Password != "" {
		return true, s.cfg.Username, s.cfg.Password
	}
	return false, "", ""
}

func (s *Server) authRequired(chain bool) bool {
	required, _, _ := s.credsFor(chain)
	return required
}

func (s *Server) checkUserPass(chain bool, user, pass string) bool {
	required, wantUser, wantPass := s.credsFor(chain)
	if !required {
		return true
	}
	// Constant-time so the proxy password cannot be recovered by timing.
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(wantPass)) == 1
	return okUser && okPass
}

func (s *Server) pickUpstream(ctx context.Context) (freproxies.Proxy, error) {
	return s.free.PickValidated(ctx, "")
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
				wired, werr := s.withVia([]freproxies.Proxy{up})
				if werr != nil {
					// Fail closed, same as the main path below.
					return nil, freproxies.Proxy{}, werr
				}
				if conn, err := dialProxyChainPool(ctx, wired, target, s.getViaPool()); err == nil {
					s.recordChannel(channel, addr, true, 0, "", time.Since(start).Milliseconds())
					return conn, lastHop(wired, up), nil
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
		wired, werr := s.withVia([]freproxies.Proxy{up})
		if werr != nil {
			// Fail closed: exit_via is set but unusable. Do not dial `up` at
			// all — that would send the client out bare while the panel claims
			// a VPS front is in place.
			return nil, freproxies.Proxy{}, werr
		}
		conn, err := dialProxyChainPool(ctx, wired, target, s.getViaPool())
		if err == nil {
			if stickyOn && clientIP != "" {
				sticky.PutProxy(clientIP, up.Addr, up.Protocol)
			}
			// A successful dial only proves the tunnel opened. For HTTPS that is all
			// this layer will ever know; the application-layer verdict, if any,
			// arrives later via the report API.
			s.recordChannel(channel, up.Addr, true, 0, "", time.Since(start).Milliseconds())
			return conn, lastHop(wired, up), nil
		}
		lastErr = err
		// A dead front node must stop the loop, not walk it: exit mode surfaces
		// the dead VPS as a failed CONNECT at `up`, and scoring `up` for it
		// walks the whole pool down one proxy per request.
		if via, ok := s.viaConfig(); ok {
			if fe := frontFailure(via, err); fe != nil {
				return nil, freproxies.Proxy{}, fe
			}
		}
		// With exit_via configured, `wired` is [VPS, up]: a dead VPS fails every
		// attempt, and scoring `up` for it walked the whole pool down one proxy
		// per request. Only penalise `up` when `up` is what failed.
		blame, ok := culpritHop(err)
		if !ok || blame.Addr == up.Addr {
			_ = s.free.Store().MarkValidated(ctx, up.Addr, 0, false)
			// Global score already took the hit above; this records that the failure
			// happened against *this* destination, which is what scopes the ban.
			s.recordChannel(channel, up.Addr, false, 0, errTag(err), 0)
		}
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
		wired, werr := s.withVia(hops)
		if werr != nil {
			// Fail closed: exit_via is set but unusable — do not dial the pool
			// direct, the panel claims a VPS front is in place.
			return nil, nil, werr
		}
		conn, err := dialProxyChainPool(dialCtx, wired, target, s.getViaPool())
		if err == nil {
			return conn, wired, nil
		}
		lastErr = err
		// Stop early when the failure is really the front node's: with a dead
		// VPS every remaining attempt fails the same way (and in exit mode each
		// would be mis-blamed on the hop that spoke the CONNECT).
		if via, ok := s.viaConfig(); ok {
			if fe := frontFailure(via, err); fe != nil {
				return nil, nil, fe
			}
		}
		// Score the hop that actually broke. Blaming hops[0] unconditionally
		// deleted healthy entry proxies while the failing hop kept ScoreMax and
		// was picked again on the next attempt. The via/VPS hop is user config,
		// not a pool member, so it is never scored here.
		if blame, ok := culpritHop(err); ok && blame.Source != "exit_via" && blame.Addr != "" {
			_ = s.free.Store().MarkValidated(dialCtx, blame.Addr, 0, false)
			if s.free.Hot() != nil {
				s.free.Hot().Invalidate(blame.Addr)
			}
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
	if s.authRequired(chain) {
		user, pass, ok := parseBasicProxyAuth(req.Header.Get("Proxy-Authorization"))
		if !ok || !s.checkUserPass(chain, user, pass) {
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
		return relayTraffic(client, up, ch, finished, s.throttleFor(chain))
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
	// httpConnectOver cleared the upstream deadline; without one, a free proxy
	// that answers CONNECT and then stalls pins this goroutine and both sockets
	// forever.
	hopTimeout := s.hopTimeout()
	_ = upConn.SetDeadline(time.Now().Add(hopTimeout))
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
	// Body streaming gets its own budget; the 30s connection-wide deadline set
	// in handle would otherwise sever a legitimate slow or large download.
	_ = upConn.SetDeadline(time.Now().Add(5 * time.Minute))
	_ = client.SetDeadline(time.Now().Add(5 * time.Minute))
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

// validHostname rejects anything that cannot appear in a hostname, most
// importantly CR/LF and spaces.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if c <= 0x20 || c == 0x7f {
			return false
		}
		switch c {
		case '/', '\\', '@', '?', '#', '"', '\'', '<', '>':
			return false
		}
	}
	return true
}

// hopTimeout is the per-hop budget from ChainOptions, used for upstream
// request/response exchanges on the plain-HTTP path too.
func (s *Server) hopTimeout() time.Duration {
	s.mu.RLock()
	ms := s.chainOpts.HopTimeoutMS
	s.mu.RUnlock()
	if ms <= 0 {
		ms = 5000
	}
	// Reading a full response can legitimately take longer than one hop dial.
	return time.Duration(ms) * time.Millisecond * 4
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
	useUserPass := s.authRequired(chain)
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
		if !s.checkUserPass(chain, string(user), string(pass)) {
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
		// The exit hop may be an HTTP proxy, where this lands verbatim in a
		// "CONNECT host:port" line — a CR/LF here would smuggle extra request
		// lines upstream and corrupt channel accounting.
		if !validHostname(host) {
			_, _ = client.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return fmt.Errorf("invalid socks5 hostname")
		}
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
	return relayTraffic(client, up, ch, finished, s.throttleFor(chain))
}

func relayTraffic(client, upstream net.Conn, channel string, finished *bool, t *throttle) error {
	// 入站已在 handle 中 BeginInbound；此处记出站并在结束后成对释放
	if finished != nil {
		*finished = true
	}
	client = throttled(client, t)
	traffic.Default.BeginOutbound(channel)
	up, down, err := traffic.BidirectionalRelay(client, upstream)
	ok := err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
	traffic.Default.EndConn(channel, ok, up, down, true)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
