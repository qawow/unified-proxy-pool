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

// trustedProxyNets are the peers whose X-Forwarded-For / X-Real-IP we believe.
// Empty (the default) means "believe nobody": the TCP peer address is the client
// address. Trusting loopback unconditionally was not safe — a reverse proxy on
// the same host relays whatever header the internet client sent unless the
// operator explicitly overwrites it, which would let that client claim a LAN
// address and walk through requireLAN.
func (a *App) trustedProxyNets(*http.Request) []*net.IPNet {
	if a == nil {
		return nil
	}
	// Read from the cached value, never from settings: this runs in the global
	// middleware, and settings.FeatureConfig is a SQLite query plus a JSON parse
	// — one per request, including static assets.
	nets, _ := a.trustedNets.Load().(*[]*net.IPNet)
	if nets == nil {
		return nil
	}
	return *nets
}

// setTrustedProxyCIDRs refreshes the cache; called at startup and whenever
// settings are saved.
func (a *App) setTrustedProxyCIDRs(cidrs []string) {
	nets := parseCIDRs(cidrs)
	a.trustedNets.Store(&nets)
}

// strictRealIP rewrites RemoteAddr from forwarded headers, but only for peers
// the operator listed in feature.trusted_proxy_cidrs. It walks X-Forwarded-For
// from the right and takes the first entry that is not itself a trusted proxy —
// the leftmost entries are attacker-supplied when the front proxy appends
// (nginx's stock $proxy_add_x_forwarded_for does).
//
// Every other client-IP consumer reads r.RemoteAddr, so this is the single
// place where that trust decision is made.
func (a *App) strictRealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host, port = r.RemoteAddr, "0"
		}
		trusted := a.trustedProxyNets(r)
		if len(trusted) > 0 && ipInNets(net.ParseIP(host), trusted) {
			if fwd := forwardedClientIP(r, trusted); fwd != "" {
				r.RemoteAddr = net.JoinHostPort(fwd, port)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func forwardedClientIP(r *http.Request, trusted []*net.IPNet) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if ipInNets(ip, trusted) {
				continue // another hop of our own proxy chain
			}
			return candidate
		}
		return ""
	}
	if x := strings.TrimSpace(r.Header.Get("X-Real-IP")); x != "" {
		if net.ParseIP(x) != nil {
			return x
		}
	}
	return ""
}

// publicClientIP is the client address as decided by strictRealIP; headers are
// never re-read here.
func publicClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
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
	// Drop stale windows: with public_open the key space is the internet.
	if len(publicSubmitLimiter.m) > 4096 {
		for k, v := range publicSubmitLimiter.m {
			if v.second < now-2 {
				delete(publicSubmitLimiter.m, k)
			}
		}
	}
	return w.count <= publicSubmitLimit
}

const (
	loginAttemptLimit  = 10
	loginAttemptWindow = 5 * time.Minute
)

var loginLimiter = struct {
	mu sync.Mutex
	m  map[string]loginWindow
}{m: map[string]loginWindow{}}

type loginWindow struct {
	until time.Time
	count int
}

// allowLoginAttempt throttles password guessing per client IP. The panel ships
// with a known default password and its login had no limiter at all, so an
// unattended box could be brute-forced from the LAN at full speed.
func allowLoginAttempt(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	now := time.Now()
	loginLimiter.mu.Lock()
	defer loginLimiter.mu.Unlock()
	w := loginLimiter.m[ip]
	if now.After(w.until) {
		w = loginWindow{until: now.Add(loginAttemptWindow)}
	}
	w.count++
	loginLimiter.m[ip] = w
	if len(loginLimiter.m) > 4096 {
		for k, v := range loginLimiter.m {
			if now.After(v.until) {
				delete(loginLimiter.m, k)
			}
		}
	}
	return w.count <= loginAttemptLimit
}

// noteLoginSuccess clears the counter so a legitimate user who mistyped a few
// times is not locked out after logging in.
func noteLoginSuccess(ip string) {
	if ip == "" {
		ip = "unknown"
	}
	loginLimiter.mu.Lock()
	delete(loginLimiter.m, ip)
	loginLimiter.mu.Unlock()
}

// securityHeaders sets the baseline response headers. The panel is a
// same-origin SPA with a session cookie, so framing and content sniffing have
// no legitimate use here.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			// The SPA is fully self-hosted; nothing loads from a third party.
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}
