package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Default LAN ranges. /api/public and /api/public/debug stay reachable from
// the home network without a login, but not from the internet unless
// feature.public_open is set or the client is in allowed_cidrs.
var defaultLANNets = mustCIDRs(
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"fc00::/7",
	"fe80::/10",
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

func parseCIDRs(cidrs []string) []*net.IPNet {
	var out []*net.IPNet
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
			out = append(out, n)
		}
	}
	return out
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isLoopbackIP(ip net.IP) bool {
	return ip != nil && ip.IsLoopback()
}

// publicClientIP does not blindly trust X-Real-IP / X-Forwarded-For: those
// headers are only honoured when the TCP peer is loopback (local reverse proxy).
// strictRealIP only honours X-Real-IP / X-Forwarded-For when the TCP peer is
// loopback. chi's RealIP would let any client spoof a LAN address and pass the gate.
func strictRealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host, port = r.RemoteAddr, "0"
		}
		if isLoopbackIP(net.ParseIP(host)) {
			fwd := strings.TrimSpace(r.Header.Get("X-Real-IP"))
			if fwd == "" {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					if i := strings.IndexByte(xff, ','); i >= 0 {
						xff = xff[:i]
					}
					fwd = strings.TrimSpace(xff)
				}
			}
			if fwd != "" {
				r.RemoteAddr = net.JoinHostPort(fwd, port)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func publicClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if isLoopbackIP(peer) {
		if x := strings.TrimSpace(r.Header.Get("X-Real-IP")); x != "" {
			return x
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
	}
	return host
}

func (a *App) publicOpen(r *http.Request) bool {
	if a == nil || a.settings == nil {
		return false
	}
	return a.settings.FeatureConfig(r.Context()).PublicOpen
}

func (a *App) allowPublicIP(r *http.Request) bool {
	if a.publicOpen(r) {
		return true
	}
	ip := net.ParseIP(publicClientIP(r))
	if ipInNets(ip, defaultLANNets) {
		return true
	}
	if a.settings == nil {
		return false
	}
	extra := parseCIDRs(a.settings.FeatureConfig(r.Context()).AllowedCIDRs)
	return ipInNets(ip, extra)
}

func (a *App) requireLAN(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowPublicIP(r) {
			writeJSON(w, http.StatusForbidden, apiResponse{
				Success: false,
				Message: "public API is LAN-only; set feature.public_open or add your CIDR in allowed_cidrs",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

const publicSubmitLimit = 20

var publicSubmitLimiter = struct {
	mu sync.Mutex
	m  map[string]ipWindow
}{m: map[string]ipWindow{}}

func allowPublicSubmit(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	now := time.Now().Unix()
	publicSubmitLimiter.mu.Lock()
	defer publicSubmitLimiter.mu.Unlock()
	w := publicSubmitLimiter.m[ip]
	if w.second != now {
		w = ipWindow{second: now, count: 0}
	}
	w.count++
	publicSubmitLimiter.m[ip] = w
	return w.count <= publicSubmitLimit
}
