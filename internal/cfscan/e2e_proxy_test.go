package cfscan

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// TestScanE2EViaHTTPConnectProxy is the end-to-end shape of the real
// deployment: the panel's own network blocks outbound :443, but the proxy exit
// does not. The scan must therefore reach a "CF edge" through an HTTP CONNECT
// proxy and record a hit — proving both the dialer and the two scan phases
// honour it.
func TestScanE2EViaHTTPConnectProxy(t *testing.T) {
	// 1. The "CF edge": a TLS server answering /cdn-cgi/trace.
	trace := serveTLSTrace(t, "fl=99f\ncolo=LAX\nsliver=a\n")
	defer trace.Close()
	_, tp, _ := net.SplitHostPort(trace.Addr().String())

	// 2. An HTTP CONNECT proxy that tunnels to whatever is asked.
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
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
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				req := string(buf[:n])
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 3. Build the dialer the way Start does.
	d, err := buildDialer("http://" + proxyLn.Addr().String())
	if err != nil {
		t.Fatalf("buildDialer: %v", err)
	}

	// 4. TCP phase through the proxy: the trace server's port must be open via
	// the tunnel; an unreachable port must not be.
	var tracePort int
	fmt.Sscanf(tp, "%d", &tracePort)
	if !tcpOpen(ctx, "127.0.0.1", tracePort, 3*time.Second, d) {
		t.Fatal("tcpOpen through the proxy failed; the CONNECT tunnel is broken")
	}
	if tcpOpen(ctx, "127.0.0.1", 1, 3*time.Second, d) {
		t.Fatal("port 1 must not be open through the proxy")
	}

	// 5. TLS phase through the same dialer must handshake and parse the trace.
	// This is the assertion that fails when the CONNECT response terminator is
	// left in the stream: the handshake reads 0x0d first and rejects the record.
	h, ok := tlsCFProbe(ctx, "127.0.0.1", tracePort, "speed.cloudflare.com", 3*time.Second, 3*time.Second, d)
	if !ok {
		t.Fatal("tlsCFProbe through the proxy recorded no hit; tunnel bytes are corrupted")
	}
	if h.Colo != "LAX" {
		t.Fatalf("colo=%s, want LAX", h.Colo)
	}
	t.Logf("HIT via HTTP-CONNECT proxy: ip=%s colo=%s latency=%dms", h.IP, h.Colo, h.LatencyMS)
}
