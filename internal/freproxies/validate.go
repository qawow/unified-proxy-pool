package freproxies

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"unified-proxy-pool/internal/netutil"
)

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

func checkHTTPProxy(ctx context.Context, p Proxy, validateURL string, timeout time.Duration) (int64, bool) {
	if validateURL == "" {
		validateURL = "http://httpbin.org/ip"
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   p.Addr,
	}
	if p.Protocol == "socks5" || p.Protocol == "socks4" {
		proxyURL.Scheme = "socks5"
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
		TLSHandshakeTimeout: timeout,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		DisableKeepAlives:   true,
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, validateURL, nil)
	if err != nil {
		return 0, false
	}
	netutil.ApplyDefaultHeaders(req.Header)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return latency, true
	}
	return latency, false
}
