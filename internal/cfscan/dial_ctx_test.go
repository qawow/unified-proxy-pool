package cfscan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// tcpOpen must actually bound each probe: the scan ctx is cancel-only, so the
// configured tcp_timeout_ms has to become a real deadline on the dial ctx.
func TestTCPOpenHonoursTimeout(t *testing.T) {
	var seen time.Duration
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("probe ctx has no deadline")
			return nil, context.Canceled
		}
		seen = time.Until(dl)
		return nil, context.DeadlineExceeded
	}
	tcpOpen(context.Background(), "192.0.2.1", 443, 25*time.Millisecond, dial)
	if seen <= 0 || seen > time.Second {
		t.Fatalf("probe deadline %v not near the configured 25ms", seen)
	}
}

// An https:// proxy URL must put a TLS layer between us and the proxy: the
// first bytes on the wire are a ClientHello, not a plaintext CONNECT that
// would leak Proxy-Authorization credentials.
func TestHTTPSProxySpeaksTLSNotPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		got <- buf[:n]
	}()
	d, err := buildDialer("https://alice:secret@" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if conn, err := d(ctx, "tcp", "198.51.100.1:443"); conn != nil {
		conn.Close()
	} else if err == nil {
		t.Fatal("dial to a non-TLS listener unexpectedly succeeded")
	}
	first := <-got
	if len(first) < 3 || first[0] != 0x16 || first[1] != 0x03 {
		t.Fatalf("first bytes %x are not a TLS handshake record — CONNECT went out in plaintext", first)
	}
	if strings.Contains(string(first), "CONNECT") || strings.Contains(string(first), "Proxy-Authorization") {
		t.Fatalf("credentials leaked in plaintext: %q", first)
	}
}

// A verified-TLS https proxy must complete CONNECT inside TLS and hand back a
// working tunnel; a proxy whose certificate does not verify must be refused
// before any credentials are sent.
func TestHTTPSProxyConnectsOverVerifiedTLS(t *testing.T) {
	trace := serveTLSTrace(t, "fl=99f\ncolo=FRA\nsliver=a\n")
	defer trace.Close()

	cert := selfCert(t)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	var sawAuth, sawConnect bool
	proxyLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go func() {
		for {
			c, err := proxyLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				n, err := c.Read(buf) // performs the TLS handshake
				if err != nil {
					return
				}
				req := string(buf[:n])
				sawConnect = strings.HasPrefix(req, "CONNECT ")
				sawAuth = strings.Contains(req, "Proxy-Authorization: Basic ")
				var target string
				if _, err := fmt.Sscanf(req, "CONNECT %s ", &target); err != nil {
					return
				}
				dst, err := net.Dial("tcp", target)
				if err != nil {
					_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				defer dst.Close()
				_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				go func() { _, _ = io.Copy(dst, c) }()
				_, _ = io.Copy(c, dst)
			}(c)
		}
	}()

	old := proxyTLSConfig
	proxyTLSConfig = func(hostname string) *tls.Config {
		return &tls.Config{ServerName: "speed.cloudflare.com", RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	defer func() { proxyTLSConfig = old }()

	d, err := buildDialer("https://alice:secret@" + proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, port, _ := net.SplitHostPort(trace.Addr().String())
	var p int
	fmt.Sscanf(port, "%d", &p)
	h, ok := tlsCFProbe(ctx, "127.0.0.1", p, "speed.cloudflare.com", 3*time.Second, 3*time.Second, d)
	if !ok || h.Colo != "FRA" {
		t.Fatalf("probe through https CONNECT proxy failed: ok=%v hit=%+v", ok, h)
	}
	if !sawConnect || !sawAuth {
		t.Fatalf("proxy saw connect=%v auth=%v — CONNECT did not run inside TLS", sawConnect, sawAuth)
	}

	// The same proxy with system roots (which do not trust the self-signed
	// fixture) must be refused before credentials cross the wire.
	proxyTLSConfig = old
	sawConnect, sawAuth = false, false
	if conn, err := d(ctx, "tcp", "127.0.0.1:"+port); conn != nil {
		conn.Close()
		t.Fatal("unverified https proxy was accepted")
	} else if err == nil {
		t.Fatal("unverified https proxy was accepted")
	}
	if sawConnect || sawAuth {
		t.Fatal("credentials were sent before certificate verification")
	}
}

// Cancelling the scan ctx must interrupt a proxy that accepted the TCP conn
// and went silent — not wait out the 15s CONNECT ceiling.
func TestConnectDialerHonoursCancel(t *testing.T) {
	gotReq := make(chan struct{}, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				if _, err := c.Read(buf); err != nil {
					return
				}
				gotReq <- struct{}{}
				_, _ = c.Read(buf) // stay silent until the client hangs up
			}(c)
		}
	}()
	d, err := buildDialer("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-gotReq
		cancel()
	}()
	start := time.Now()
	conn, err := d(ctx, "tcp", "198.51.100.1:443")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("silent proxy must not hand back a tunnel after cancel")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("cancel took %v to interrupt CONNECT", el)
	}
}

// The TLS probe's response read must also stop on cancel: a server that
// handshakes then goes silent used to hold the scan slot until the deadline.
func TestTLSProbeReadHonoursCancel(t *testing.T) {
	ln := serveTLSTraceSilent(t)
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var p int
	fmt.Sscanf(port, "%d", &p)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, ok := tlsCFProbe(ctx, "127.0.0.1", p, "speed.cloudflare.com", 30*time.Second, 30*time.Second, directDial); ok {
		t.Fatal("silent server must not produce a hit")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("cancel took %v to interrupt the response read", el)
	}
}

// serveTLSTraceSilent accepts TLS and reads the request but never answers.
func serveTLSTraceSilent(t *testing.T) net.Listener {
	t.Helper()
	cert := selfCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				_, _ = c.Read(buf)
				_, _ = c.Read(buf) // stay silent until the client hangs up
			}(c)
		}
	}()
	return ln
}
