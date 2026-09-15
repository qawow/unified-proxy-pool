package cfscan

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBuildDialerDirect: no proxy means a plain dialer, and must not error —
// Start treats an error here as a hard failure of the whole scan.
func TestBuildDialerDirect(t *testing.T) {
	d, err := buildDialer("")
	if err != nil {
		t.Fatalf("buildDialer(\"\") err = %v", err)
	}
	if d == nil {
		t.Fatal("buildDialer(\"\") returned nil dialer")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := d(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("direct dial failed: %v", err)
	}
	c.Close()
}

// TestBuildDialerRejectsShortcut: the panel must expand "pool"/"chain" into a
// concrete URL before calling Start. Accepting the shortcut silently would
// make the scan run direct when the operator asked for the proxy exit, which
// is the exact failure mode this feature exists to avoid.
func TestBuildDialerRejectsShortcut(t *testing.T) {
	for _, s := range []string{"pool", "direct", "single", "chain"} {
		if _, err := buildDialer(s); err == nil {
			t.Fatalf("buildDialer(%q) must reject the shortcut", s)
		}
	}
}

// TestBuildDialerBadURL: malformed proxy URLs must fail loudly rather than
// falling back to direct — a silent fallback scans the wrong network.
func TestBuildDialerBadURL(t *testing.T) {
	for _, s := range []string{"not a url", "://nohost", "ftp://example.com:21"} {
		if _, err := buildDialer(s); err == nil {
			t.Fatalf("buildDialer(%q) must error", s)
		}
	}
}

// TestHTTPConnectDialerTunnels: the HTTP CONNECT path must hand back a working
// tunnel the TLS probe can use. This is what makes an HTTP proxy usable for a
// scan that speaks its own TLS handshake.
func TestHTTPConnectDialerTunnels(t *testing.T) {
	got := make(chan string, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 256)
		n, _ := c.Read(buf)
		got <- string(buf[:n])
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		// Echo so the caller can see the tunnel carries data.
		_, _ = io.Copy(c, c)
	}()

	u, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	d := httpConnectDialer(u)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := d(ctx, "tcp", "1.2.3.4:443")
	if err != nil {
		t.Fatalf("CONNECT dial: %v", err)
	}
	defer c.Close()

	select {
	case req := <-got:
		if !strings.Contains(req, "CONNECT 1.2.3.4:443") {
			t.Fatalf("CONNECT request wrong: %q", req)
		}
	default:
		t.Fatal("proxy never saw a CONNECT request")
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("tunnel write: %v", err)
	}
}

// TestHTTPConnectDialerRejectsNon200: a proxy that refuses the tunnel must
// return an error, not a half-open conn the scan would then hang on.
func TestHTTPConnectDialerRejectsNon200(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 256)
		_, _ = c.Read(buf)
		_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
	}()

	u, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	d := httpConnectDialer(u)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := d(ctx, "tcp", "1.2.3.4:443"); err == nil {
		t.Fatal("CONNECT that returned 407 must surface an error")
	}
}

// TestScanTCPHonoursDialer proves the scan never touches the network
// directly when the dialer is a proxy: both phases go through it.
func TestScanTCPHonoursDialer(t *testing.T) {
	var attempts atomic.Int32
	failDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		attempts.Add(1)
		return nil, errors.New("no direct allowed")
	}
	s := New(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	open := s.scanTCP(ctx, []string{"1.2.3.4", "5.6.7.8"}, 4, time.Second, failDial)
	if len(open) != 0 {
		t.Fatalf("expected no open ports through a failing dialer, got %d", len(open))
	}
	if attempts.Load() != 2 {
		t.Fatalf("dialer called %d times, want 2", attempts.Load())
	}
}
