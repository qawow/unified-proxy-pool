package probe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"unified-proxy-pool/internal/freproxies"
	"unified-proxy-pool/internal/models"
)

func isNativeProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http", "https", "socks", "socks4", "socks5":
		return true
	default:
		return false
	}
}

func joinHostPort(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func tcpDialMS(ctx context.Context, host string, port int, timeout time.Duration) (int64, error) {
	addr := joinHostPort(host, port)
	if addr == "" {
		return 0, fmt.Errorf("tcp: empty address")
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return ms, fmt.Errorf("tcp %s: %w", addr, err)
	}
	_ = conn.Close()
	return ms, nil
}

type nodeCreds struct {
	TLS bool
}

func credsFromNormalizedJSON(raw string) nodeCreds {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nodeCreds{}
	}
	return nodeCreds{TLS: boolish(m["tls"])}
}

func boolish(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	case float64:
		return t != 0
	default:
		return false
	}
}

func probeNativeHTTP(ctx context.Context, node models.RuntimeNode, testURL string, timeout time.Duration) (int64, error) {
	proto := strings.ToLower(strings.TrimSpace(node.Protocol))
	if proto == "socks" {
		proto = "socks5"
	}
	p := freproxies.Proxy{
		Host:     node.Server,
		Port:     node.Port,
		Addr:     joinHostPort(node.Server, node.Port),
		Protocol: proto,
	}
	ms, ok := freproxies.CheckProxy(ctx, p, testURL, timeout)
	if !ok {
		return ms, fmt.Errorf("native %s probe failed via %s", proto, testURL)
	}
	return ms, nil
}

func probeTLSHandshake(ctx context.Context, host string, port int, timeout time.Duration) (int64, error) {
	addr := joinHostPort(host, port)
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return time.Since(start).Milliseconds(), fmt.Errorf("tcp %s: %w", addr, err)
	}
	defer conn.Close()
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec
	_ = tlsConn.SetDeadline(time.Now().Add(timeout))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return time.Since(start).Milliseconds(), fmt.Errorf("tls %s: %w", addr, err)
	}
	return time.Since(start).Milliseconds(), nil
}

// measureNodeLatency uses a cheap protocol-specific check:
// native HTTP/SOCKS → validator GET; socks5+tls → TLS handshake;
// tunnel protocols → TCP then Mihomo delay API.
func (s *Service) measureNodeLatency(ctx context.Context, node models.RuntimeNode, secret, testURL string, timeoutMS int) (int64, error) {
	if timeoutMS <= 0 {
		timeoutMS = 8000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	proto := strings.ToLower(strings.TrimSpace(node.Protocol))
	creds := credsFromNormalizedJSON(node.NormalizedJSON)

	if isNativeProtocol(proto) {
		if proto == "socks5" && creds.TLS {
			return probeTLSHandshake(ctx, node.Server, node.Port, timeout)
		}
		return probeNativeHTTP(ctx, node, testURL, timeout)
	}

	if _, err := tcpDialMS(ctx, node.Server, node.Port, timeout); err != nil {
		return 0, err
	}
	delay, err := s.mihomo.Delay(ctx, secret, runtimeNodeName(node), testURL, timeoutMS)
	if err != nil {
		return 0, fmt.Errorf("mihomo delay: %w", err)
	}
	return delay, nil
}
