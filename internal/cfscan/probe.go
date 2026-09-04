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

func tcpOpen(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func tlsCFProbe(ctx context.Context, ip string, port int, sni string, handshake, read time.Duration) (Hit, bool) {
	d := &net.Dialer{Timeout: handshake}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
	if err != nil {
		return Hit{}, false
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(handshake + read))
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
