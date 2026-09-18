package cfscan

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// buildDialer returns a dialer for the scan. An empty proxyURL dials directly;
// otherwise the scan reaches :443 through the proxy, which is what makes the
// feature usable on a network that blocks direct 443.
//
// "pool", "direct", "chain", or a bare port is also accepted, matching the
// shortcuts the subscription fetcher uses, so the panel can say "scan via the
// pool's exit" without knowing the URL.
func buildDialer(proxyURL string) (dialFunc, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" || strings.EqualFold(proxyURL, "none") {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext, nil
	}

	switch strings.ToLower(proxyURL) {
	case "direct", "pool", "single":
		return nil, fmt.Errorf("%q needs a concrete URL here; the panel must expand it before calling Start", proxyURL)
	}

	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad proxy url %q", proxyURL)
	}

	switch strings.ToLower(u.Scheme) {
	case "socks", "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 %s: %w", u.Host, err)
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 %s: not a context dialer", u.Host)
		}
		return cd.DialContext, nil
	case "http", "https":
		// HTTP CONNECT tunnels give a net.Conn the TLS probe can use directly.
		return httpConnectDialer(u), nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}

// httpConnectDialer dials through an HTTP CONNECT proxy. The scan needs a raw
// TCP conn to run its own TLS handshake against, so it cannot use an
// http.Transport — it has to speak CONNECT itself and hand back the tunnel.
func httpConnectDialer(u *url.URL) dialFunc {
	target := u.Host
	if u.Port() == "" {
		if strings.EqualFold(u.Scheme, "https") {
			target += ":443"
		} else {
			target += ":80"
		}
	}
	auth := ""
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = "Basic " + base64Std(u.User.Username()+":"+pass)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		raw, err := d.DialContext(ctx, network, target)
		if err != nil {
			return nil, err
		}
		// Bound the whole proxy setup — optional TLS handshake plus the
		// CONNECT exchange — by the earlier of the scan ctx's deadline and
		// connectTimeout, and close the conn when the scan is stopped. The
		// scan's ctx often has no deadline (the TCP phase runs cancel-only),
		// so without this a silent proxy pins a scan slot for the full 15s —
		// or, before this bound existed, forever.
		dl := time.Now().Add(connectTimeout)
		if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
			dl = cd
		}
		_ = raw.SetDeadline(dl)
		defer raw.SetDeadline(time.Time{})
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()

		var c net.Conn = raw
		if strings.EqualFold(u.Scheme, "https") {
			// HTTPS proxies get a verified TLS layer before CONNECT. The
			// request may carry Proxy-Authorization credentials, which must
			// never travel over plaintext or an unverified channel — the old
			// code sent them in the clear.
			tc := tls.Client(raw, proxyTLSConfig(u.Hostname()))
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, fmt.Errorf("https proxy %s TLS: %w", target, err)
			}
			c = tc
		}

		req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
		if auth != "" {
			req += "Proxy-Authorization: " + auth + "\r\n"
		}
		req += "\r\n"
		if _, err := c.Write([]byte(req)); err != nil {
			_ = raw.Close()
			return nil, err
		}
		// Read the whole response header, through the blank line. Stopping at
		// the first CRLF leaves the terminating "\r\n" in the stream, and the
		// TLS probe then reads 0x0d as its first record byte and fails every
		// handshake — the tunnel works but no scan ever hits.
		buf := make([]byte, 0, 256)
		b := make([]byte, 1)
		for len(buf) < 256 {
			if _, err := c.Read(b); err != nil {
				_ = raw.Close()
				return nil, err
			}
			buf = append(buf, b[0])
			if headerEnded(buf) {
				break
			}
		}
		line := strings.TrimSpace(firstLine(buf))
		if !strings.Contains(line, " 200 ") {
			_ = raw.Close()
			return nil, fmt.Errorf("CONNECT %s failed: %s", addr, line)
		}
		return c, nil
	}
}

// proxyTLSConfig builds the TLS config used to reach an https:// CONNECT
// proxy. Certificate verification stays enabled — CONNECT carries
// credentials that must never cross an unverified channel. Tests may swap
// in a pool trusting a local fixture.
var proxyTLSConfig = func(hostname string) *tls.Config {
	return &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12}
}

// connectTimeout bounds a CONNECT handshake. Generous, because the scan's own
// per-probe timeout still applies on top, but finite: without it a silent
// proxy pins a scan slot forever.
const connectTimeout = 15 * time.Second

// headerEnded reports whether buf ends with the blank line that terminates a
// CONNECT response header, accepting both CRLF and bare-LF servers.
func headerEnded(buf []byte) bool {
	return bytes.HasSuffix(buf, []byte("\r\n\r\n")) || bytes.HasSuffix(buf, []byte("\n\n"))
}

// firstLine returns the status line from a response header buffer.
func firstLine(buf []byte) string {
	if i := bytes.IndexAny(buf, "\r\n"); i >= 0 {
		return string(buf[:i])
	}
	return string(buf)
}
