package directproxy

import (
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
