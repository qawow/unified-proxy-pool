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
	case "socks", "socks4", "socks5", "http", "https":
		if proto == "socks" || proto == "https" {
			if proto == "https" {
				proto = "http"
			} else {
				proto = "socks5"
			}
		}
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
	if strings.EqualFold(strings.TrimSpace(mode), "entry") {
		return append([]freproxies.Proxy{via}, hops...)
	}
	return append(append([]freproxies.Proxy{}, hops...), via)
}

func (s *Server) chainPathWithVia(hops int) string {
	base := ChainPathLabel(hops)
	opts := s.GetChainOptions()
	if strings.TrimSpace(opts.ExitVia) == "" {
		return base
	}
	if strings.EqualFold(strings.TrimSpace(opts.ExitViaMode), "entry") {
		return "本机 → VPS → " + strings.TrimPrefix(base, "本机 → ")
	}
	return strings.TrimSuffix(base, " → 目标") + " → VPS → 目标"
}

func (s *Server) withVia(hops []freproxies.Proxy) []freproxies.Proxy {
	opts := s.GetChainOptions()
	raw := strings.TrimSpace(opts.ExitVia)
	if raw == "" {
		return hops
	}
	via, err := ParseViaProxy(raw)
	if err != nil || via.Addr == "" {
		return hops
	}
	return attachVia(hops, via, opts.ExitViaMode)
}
