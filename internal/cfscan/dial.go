package cfscan

import (
	"context"
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
		c, err := d.DialContext(ctx, network, target)
		if err != nil {
			return nil, err
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(dl)
		}
		req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
		if auth != "" {
			req += "Proxy-Authorization: " + auth + "\r\n"
		}
		req += "\r\n"
		if _, err := c.Write([]byte(req)); err != nil {
			_ = c.Close()
			return nil, err
		}
		// Read the CONNECT response line; a tunnel that never answers hangs the
		// scan, so bound it with the deadline set above.
		buf := make([]byte, 0, 256)
		b := make([]byte, 1)
		for len(buf) < 256 {
			if _, err := c.Read(b); err != nil {
				_ = c.Close()
				return nil, err
			}
			buf = append(buf, b[0])
			if len(buf) >= 2 && buf[len(buf)-2] == '\r' && buf[len(buf)-1] == '\n' {
				break
			}
		}
		line := strings.TrimSpace(string(buf))
		if !strings.Contains(line, " 200 ") {
			_ = c.Close()
			return nil, fmt.Errorf("CONNECT %s failed: %s", addr, line)
		}
		return c, nil
	}
}
