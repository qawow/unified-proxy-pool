package directproxy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"unified-proxy-pool/internal/freproxies"
)

// ParseViaProxy turns a user-supplied VPS URL into a hop.
// Accepted: socks5://user:pass@host:1080  http://host:3128  host:port (http)
func ParseViaProxy(raw string) (freproxies.Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return freproxies.Proxy{}, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return freproxies.Proxy{}, fmt.Errorf("exit_via: %w", err)
	}
	proto := strings.ToLower(u.Scheme)
	switch proto {
	case "http", "socks4", "socks5":
	case "socks", "socks5h":
		proto = "socks5"
	case "https":
		// "https://" as a *proxy* scheme means the hop to the proxy is itself
		// wrapped in TLS — not the same thing as an HTTP proxy carrying https via
		// CONNECT. Nothing here implements it: dialFast opens a bare TCP conn and
		// httpConnectOver writes a plaintext CONNECT. Rewriting the scheme to
		// "http" (which this used to do) meant a user who asked for TLS to their
		// VPS silently got cleartext, credentials included.
		return freproxies.Proxy{}, fmt.Errorf(
			"exit_via: https:// (TLS to the proxy) is not supported; " +
				"use http:// or socks5:// — the tunnel inside is encrypted either way")
	default:
		return freproxies.Proxy{}, fmt.Errorf("exit_via: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return freproxies.Proxy{}, fmt.Errorf("exit_via: missing host")
	}
	port := 0
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if port <= 0 {
		if proto == "socks5" || proto == "socks4" {
			port = 1080
		} else {
			port = 8080
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	user := ""
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return freproxies.Proxy{
		Host:     host,
		Port:     port,
		Addr:     addr,
		Protocol: proto,
		Username: user,
		Password: pass,
		Source:   "exit_via",
	}, nil
}

func lastHop(hops []freproxies.Proxy, fallback freproxies.Proxy) freproxies.Proxy {
	if len(hops) == 0 {
		return fallback
	}
	return hops[len(hops)-1]
}

func attachVia(hops []freproxies.Proxy, via freproxies.Proxy, mode string) []freproxies.Proxy {
	if via.Addr == "" {
		return hops
	}
	if strings.EqualFold(strings.TrimSpace(mode), "exit") {
		return append(append([]freproxies.Proxy{}, hops...), via)
	}
	return append([]freproxies.Proxy{via}, hops...)
}

func (s *Server) chainPathWithVia(hops int) string {
	base := ChainPathLabel(hops)
	opts := s.GetChainOptions()
	if strings.TrimSpace(opts.ExitVia) == "" {
		return base
	}
	if strings.EqualFold(strings.TrimSpace(opts.ExitViaMode), "exit") {
		return strings.TrimSuffix(base, " → 目标") + " → VPS → 目标"
	}
	return "本机 → VPS → " + strings.TrimPrefix(base, "本机 → ")
}

func (s *Server) getViaPool() *viaPool {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.viaPool
}

// viaConfig parses the configured front node once per dial round. ok is false
// when no via is set or it does not parse (the fail-closed error path in
// withVia handles the misconfigured case before any dial happens).
func (s *Server) viaConfig() (via freproxies.Proxy, ok bool) {
	opts := s.GetChainOptions()
	if strings.TrimSpace(opts.ExitVia) == "" {
		return freproxies.Proxy{}, false
	}
	via, err := ParseViaProxy(opts.ExitVia)
	if err != nil || via.Addr == "" {
		return freproxies.Proxy{}, false
	}
	return via, true
}

// viaReachable re-checks the front node on a failure path. It runs on a fresh
// context: the dial context may be at its deadline exactly when we need this
// answer, and an expired ctx would falsely report the front as dead.
func viaReachable(via freproxies.Proxy) bool {
	ctx, cancel := context.WithTimeout(context.Background(), viaDialTimeout)
	defer cancel()
	c, err := dialFast(ctx, via.Addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// frontFailure re-attributes a chain dial error when the real culprit is a dead
// front node, returning nil when the failure genuinely belongs to a pool hop.
//
// Entry mode blames the via hop directly (Source == "exit_via"), but without a
// liveness re-check a single stale warm connection would mark the front down.
// Exit mode is the dangerous case: the VPS is the CONNECT *target* of the last
// pool hop, so a dead VPS surfaces as a failure blamed on that hop — scoring it
// would flush the whole pool for an outage that belongs to the VPS.
func frontFailure(via freproxies.Proxy, err error) error {
	if _, ok := culpritHop(err); !ok {
		return nil
	}
	if viaReachable(via) {
		return nil
	}
	return &freproxies.FrontError{Err: err}
}

// ProbeFront exposes the configured exit_via to the validator. The probe must
// traverse the same front node as client traffic: a proxy reachable from this
// host but unreachable from the VPS is dead for every chain user, and the
// reverse is a proxy direct probing would wrongly bury.
func (s *Server) ProbeFront() *freproxies.ProbeFront {
	opts := s.GetChainOptions()
	raw := strings.TrimSpace(opts.ExitVia)
	if raw == "" {
		return nil
	}
	via, err := ParseViaProxy(raw)
	if err != nil {
		return &freproxies.ProbeFront{Err: err}
	}
	if via.Addr == "" {
		return &freproxies.ProbeFront{Err: fmt.Errorf("exit_via %q resolved to no address", raw)}
	}
	return &freproxies.ProbeFront{Hop: via, Mode: opts.ExitViaMode}
}

// ChainProbeDial satisfies freproxies.ChainDialer with the production chain
// dialer. A failure owned by the front node is wrapped so the validator does
// not blame the candidate for a dead VPS.
func (s *Server) ChainProbeDial(ctx context.Context, hops []freproxies.Proxy, target string) (net.Conn, error) {
	conn, err := dialProxyChainPool(ctx, hops, target, s.getViaPool())
	if err != nil {
		if via, ok := s.viaConfig(); ok {
			if fe := frontFailure(via, err); fe != nil {
				return nil, fe
			}
		}
		return nil, err
	}
	return conn, nil
}

func (s *Server) ViaPoolStats() map[string]any {
	p := s.getViaPool()
	if p == nil {
		return map[string]any{"enabled": false}
	}
	return p.Stats()
}

func (s *Server) rebuildViaPool() {
	opts := s.GetChainOptions()
	via, err := ParseViaProxy(opts.ExitVia)
	s.mu.Lock()
	old := s.viaPool
	s.viaPool = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if err != nil || via.Addr == "" || strings.EqualFold(strings.TrimSpace(opts.ExitViaMode), "exit") {
		return
	}
	p := newViaPool(via)
	s.mu.Lock()
	s.viaPool = p
	s.mu.Unlock()
}

// withVia inserts the configured VPS hop. It fails closed: when exit_via is set
// but unusable, the dial must not proceed.
//
// Returning the bare hop list on a parse error (which this used to do) meant a
// single typo in exit_via sent traffic straight out through the free proxies
// while the panel still displayed "本机 → VPS → …". Someone relying on the VPS
// as their front would have no signal that it had been bypassed — the same
// silent-leak shape as a VPN tunnel dropping without a kill switch.
func (s *Server) withVia(hops []freproxies.Proxy) ([]freproxies.Proxy, error) {
	opts := s.GetChainOptions()
	raw := strings.TrimSpace(opts.ExitVia)
	if raw == "" {
		return hops, nil
	}
	via, err := ParseViaProxy(raw)
	if err != nil {
		return nil, err
	}
	if via.Addr == "" {
		return nil, fmt.Errorf("exit_via %q resolved to no address", raw)
	}
	return attachVia(hops, via, opts.ExitViaMode), nil
}
