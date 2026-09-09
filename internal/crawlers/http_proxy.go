package crawlers

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// NewHTTPClientWithProxy builds the scraper HTTP client.
//
//	""            — honour HTTP(S)_PROXY from the environment (old behaviour)
//	"none"/"off"  — no proxy, even if the env is set
//	http(s)://…   — HTTP CONNECT proxy
//	socks5://…    — SOCKS5 (used for clash mixed-port / VPS)
func NewHTTPClientWithProxy(timeout time.Duration, proxyRaw string) *HTTPClient {
	return newProxyClient(timeout, proxyRaw, true)
}

// NewVerifiedHTTPClientWithProxy is NewHTTPClientWithProxy with certificate
// verification left ON. Scraped proxy lists are low-trust data, so the plain
// constructor tolerates the broken certificates free proxies hand out; anything
// whose *integrity matters* (release binaries, checksums) must use this one so
// the untrusted exit node only ever sees an opaque CONNECT tunnel.
func NewVerifiedHTTPClientWithProxy(timeout time.Duration, proxyRaw string) *HTTPClient {
	return newProxyClient(timeout, proxyRaw, false)
}

func newProxyClient(timeout time.Duration, proxyRaw string, insecure bool) *HTTPClient {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // free-proxy lists are served over broken TLS
	}
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		DialContext: (&net.Dialer{
			Timeout: 6 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsCfg,
		MaxIdleConns:          32,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 6 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	applyProxy(transport, proxyRaw)
	return &HTTPClient{
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

func applyProxy(transport *http.Transport, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		transport.Proxy = http.ProxyFromEnvironment
		return
	}
	if strings.EqualFold(raw, "none") || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "direct-wan") {
		transport.Proxy = nil
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		transport.Proxy = http.ProxyFromEnvironment
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "socks", "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err == nil {
			if cd, ok := d.(proxy.ContextDialer); ok {
				transport.DialContext = cd.DialContext
				transport.Proxy = nil
				return
			}
		}
		transport.Proxy = http.ProxyURL(u)
	default:
		transport.Proxy = http.ProxyURL(u)
	}
}

// ResolveScrapeProxy maps panel shortcuts onto a dialable proxy URL.
//
//	direct / 7892 → the single-hop DirectProxy
//	chain  / 7893 → the chain proxy
//	none          → disable env proxy
//	anything else → returned as-is (http://, socks5://, …)
func ResolveScrapeProxy(raw, directListen, chainListen string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch strings.ToLower(raw) {
	case "none", "off", "direct-wan":
		return "none"
	case "direct", "pool", "7892", "single":
		return listenProxyURL(directListen)
	case "chain", "7893":
		return listenProxyURL(chainListen)
	default:
		return raw
	}
}

func listenProxyURL(listen string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || port == "" {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
