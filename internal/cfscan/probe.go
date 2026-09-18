package cfscan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

var probeSNIs = []string{
	"speed.cloudflare.com",
	"cdnjs.cloudflare.com",
}

var cfMarkers = [][]byte{
	[]byte("fl="),
	[]byte("colo="),
	[]byte("sliver="),
}

type Hit struct {
	IP        string `json:"ip"`
	Colo      string `json:"colo"`
	FL        string `json:"fl"`
	SNI       string `json:"sni"`
	LatencyMS int64  `json:"latency_ms"`
	LastSeen  string `json:"last_seen"`
}

func tcpOpen(ctx context.Context, ip string, port int, timeout time.Duration, dial dialFunc) bool {
	// The timeout argument used to be dropped: the scan-level ctx is
	// cancel-only, so tcp_timeout_ms had no effect at all and every probe
	// fell back to the dialer's own default (10s direct, up to 15s CONNECT).
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c, err := dial(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// dialFunc is the net.Dialer shape plus any proxy-backed dialer. cfscan needs
// to reach :443, and the panel's own network may block direct 443 while the
// proxy pool can still get there — so the scan dials through a proxy when one
// is configured, and directly otherwise.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func tlsCFProbe(ctx context.Context, ip string, port int, sni string, handshake, read time.Duration, dial dialFunc) (Hit, bool) {
	budget := handshake + read
	if budget > 0 {
		// Bound the whole probe — dial, handshake, write and response read —
		// so a cancel-only scan ctx cannot strand a probe inside a silent
		// proxy's SOCKS5 dial.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	raw, err := dial(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return Hit{}, false
	}
	defer raw.Close()
	// HandshakeContext observes ctx, but the write and ReadAll after it do
	// not. Closing the conn is the only reliable interrupt for a server (or
	// proxy tunnel) that accepts the handshake then goes silent, so a Stop
	// actually frees the scan slot instead of waiting out the deadline.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(budget))
	start := time.Now()
	tc := tls.Client(raw, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, //nolint:gosec
		NextProtos:         []string{"http/1.1"},
		MinVersion:         tls.VersionTLS12,
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		return Hit{}, false
	}
	req := fmt.Sprintf("GET /cdn-cgi/trace HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: unified-proxy-pool-cfscan\r\n\r\n", sni)
	if _, err := io.WriteString(tc, req); err != nil {
		return Hit{}, false
	}
	body, err := io.ReadAll(io.LimitReader(tc, 8<<10))
	if err != nil && len(body) == 0 {
		return Hit{}, false
	}
	if !isCFTrace(body) {
		return Hit{}, false
	}
	return Hit{
		IP:        ip,
		Colo:      traceField(body, "colo"),
		FL:        traceField(body, "fl"),
		SNI:       sni,
		LatencyMS: time.Since(start).Milliseconds(),
	}, true
}

func isCFTrace(body []byte) bool {
	n := 0
	for _, m := range cfMarkers {
		if bytes.Contains(body, m) {
			n++
		}
	}
	return n >= 2
}

func traceField(body []byte, key string) string {
	prefix := key + "="
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
