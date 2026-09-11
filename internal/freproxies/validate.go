package freproxies

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"unified-proxy-pool/internal/netutil"
)

// ChainDialer opens target through the given hops, speaking each hop's own
// protocol. directproxy registers the production chain dialer here, so a probe
// exercises exactly the path a client would take — a second implementation
// would drift and then probe and production would disagree about what works.
type ChainDialer func(ctx context.Context, hops []Proxy, target string) (net.Conn, error)

// ProbeFront is the fixed front node (exit_via) a validation probe must
// traverse before it reaches the candidate. Err is non-nil when a front is
// configured but unusable: the probe then fails closed, and the failure
// belongs to the front, never to the candidate.
type ProbeFront struct {
	Hop  Proxy
	Mode string // "entry": front first; "exit": front last
	Err  error
}

// ProbeFrontProvider reports the live front-node configuration. Returning nil
// means no front is configured and probes dial the candidate directly.
type ProbeFrontProvider func() *ProbeFront

// ErrFrontUnavailable marks a failure owned by the configured front node
// (exit_via) rather than by the pool proxy under test. Like ErrCheckAborted it
// must not be recorded as a candidate failure: a dead VPS says nothing about
// the proxies behind it.
var ErrFrontUnavailable = errors.New("exit_via front node unavailable")

// FrontError wraps a dial failure caused by the front node.
type FrontError struct{ Err error }

func (e *FrontError) Error() string { return ErrFrontUnavailable.Error() + ": " + e.Err.Error() }
func (e *FrontError) Unwrap() error { return e.Err }
func (e *FrontError) Is(target error) bool {
	return target == ErrFrontUnavailable
}

// probePlan routes one validation probe through the configured front node, so
// the checked path is the chain clients actually dial. A nil plan probes the
// candidate directly.
type probePlan struct {
	front Proxy
	mode  string
	dial  ChainDialer
}

// hops orders the chain the way production does: entry mode reaches the
// candidate through the front, exit mode reaches the front through the
// candidate.
func (plan probePlan) hops(p Proxy) []Proxy {
	if strings.EqualFold(plan.mode, "exit") {
		return []Proxy{p, plan.front}
	}
	return []Proxy{plan.front, p}
}

// CheckProxy runs the pool's own liveness check against a single proxy and
// returns its latency in milliseconds plus whether it worked.
//
// Exported so command-line tooling can validate a list without a running panel
// and without a second copy of the dialing rules — a separate implementation
// would drift, and then the tool would disagree with the pool about which
// proxies are alive.
func CheckProxy(ctx context.Context, p Proxy, validateURL string, timeout time.Duration) (int64, bool) {
	if p.Addr == "" {
		p.Addr = normalizeAddr(p.Host, p.Port)
	}
	return checkHTTPProxy(ctx, p, validateURL, timeout)
}

// canaryURL is fetched to prove a proxy actually forwards traffic instead of
// answering probes itself. It is HTTPS with certificates verified, so a proxy
// cannot produce a passing response without really reaching the origin.
//
// This matters: a large family of "free proxies" (batches of ports opened on
// one cloud host) reply to well-known probe URLs with a canned success payload
// and time out on everything else. Against a status-code-only check they all
// look alive. Sampling 40 of them, 40/40 failed this canary while 29/40 real
// proxies passed.
// Overridable so tests can point it at a local server.
var canaryURL = "https://cp.cloudflare.com/generate_204"

func checkHTTPProxy(ctx context.Context, p Proxy, validateURL string, timeout time.Duration) (int64, bool) {
	latency, ok, _ := checkHTTPProxyPlan(ctx, p, validateURL, timeout, nil)
	return latency, ok
}

// checkHTTPProxyPlan is checkHTTPProxy plus front-node routing and error
// visibility. The error distinguishes "the candidate failed" from "the front
// node failed" (ErrFrontUnavailable) — the two must not cost the same score.
func checkHTTPProxyPlan(ctx context.Context, p Proxy, validateURL string, timeout time.Duration, plan *probePlan) (int64, bool, error) {
	if validateURL == "" {
		validateURL = "http://httpbin.org/ip"
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	latency, ok, err := fetchThrough(ctx, p, validateURL, timeout, tlsVerifiedFor(validateURL), plan)
	if !ok {
		return latency, false, err
	}
	// A plaintext validate URL proves nothing on its own — the proxy sees the
	// whole exchange and can fabricate any status it likes. Make it prove itself
	// over a channel it cannot forge. An HTTPS validate URL is already that
	// proof, so skip the extra round trip.
	if isPlaintextURL(validateURL) {
		if _, ok, err := fetchThrough(ctx, p, canaryURL, timeout, true, plan); !ok {
			return latency, false, err
		}
	}
	return latency, true, nil
}

// tlsVerifiedFor reports whether certificate verification should be enforced for
// url. Verification is what makes an HTTPS check unforgeable, so it is on for
// every https target.
func tlsVerifiedFor(rawURL string) bool { return !isPlaintextURL(rawURL) }

func isPlaintextURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	return !strings.EqualFold(u.Scheme, "https")
}

func fetchThrough(ctx context.Context, p Proxy, target string, timeout time.Duration, verifyTLS bool, plan *probePlan) (int64, bool, error) {
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   p.Addr,
	}
	// Authenticated proxies (manual nodes, paid endpoints) were dialled without
	// credentials and so always reported "unavailable".
	if p.Username != "" || p.Password != "" {
		proxyURL.User = url.UserPassword(p.Username, p.Password)
	}
	socks4 := netutil.IsSOCKS4(p.Protocol)
	if strings.EqualFold(p.Protocol, "socks5") || strings.EqualFold(p.Protocol, "socks") {
		proxyURL.Scheme = "socks5"
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
		TLSHandshakeTimeout: timeout,
		// Verification is deliberately on for https targets: an unverified TLS
		// check is forgeable by any proxy that MITMs the CONNECT, which is
		// exactly the class of proxy this check exists to reject.
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: !verifyTLS, MinVersion: tls.VersionTLS12}, //nolint:gosec
		DisableKeepAlives: true,
	}
	switch {
	case plan != nil:
		// Chain probe: the dialer already speaks every hop's protocol (the
		// candidate's included), so the transport must not layer its own proxy
		// handling on top — Proxy stays nil and dialling is the chain.
		transport.Proxy = nil
		transport.DialContext = func(cctx context.Context, _, addr string) (net.Conn, error) {
			return plan.dial(cctx, plan.hops(p), addr)
		}
	case socks4:
		// net/http has no SOCKS4 support, so tunnel every dial ourselves and
		// leave Proxy unset. Without this, socks4 lists could never validate.
		addr := p.Addr
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, dialAddr string) (net.Conn, error) {
			return netutil.DialSOCKS4(ctx, &net.Dialer{Timeout: timeout}, addr, dialAddr)
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, false, err
	}
	netutil.ApplyDefaultHeaders(req.Header)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	// Only a success counts. Accepting every status below 500 meant a proxy that
	// answered "407 Proxy Authentication Required" or served its own "403
	// forbidden" block page was recorded as a working proxy — it reached the
	// pool and then failed for every real request.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return latency, true, nil
	}
	// 3xx is only meaningful if the client stopped following redirects, which
	// happens for the "too many redirects" guard above; treat it as a failure.
	return latency, false, fmt.Errorf("probe status %d", resp.StatusCode)
}
