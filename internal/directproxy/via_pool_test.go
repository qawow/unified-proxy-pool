package directproxy

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

func startTestSOCKS(t *testing.T) (addr string, handshakes *atomic.Int64, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 3)
				if _, err := io.ReadFull(conn, buf); err != nil {
					return
				}
				n.Add(1)
				if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
					return
				}
				hdr := make([]byte, 4)
				if _, err := io.ReadFull(conn, hdr); err != nil {
					select {
					case <-done:
					default:
					}
					return
				}
				// drain rest of CONNECT then reply success to 0.0.0.0:0
				if hdr[3] == 0x03 {
					l := make([]byte, 1)
					if _, err := io.ReadFull(conn, l); err == nil {
						_, _ = io.ReadFull(conn, make([]byte, int(l[0])+2))
					}
				}
				_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				<-done
			}(c)
		}
	}()
	return ln.Addr().String(), &n, func() { close(done); _ = ln.Close() }
}

func TestViaPoolReusesHandshakes(t *testing.T) {
	addr, n, stop := startTestSOCKS(t)
	defer stop()
	host, _, _ := net.SplitHostPort(addr)
	p := newViaPool(freproxies.Proxy{Addr: addr, Host: host, Protocol: "socks5", Source: "exit_via"})
	defer p.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Stats()
		if idle, _ := st["idle"].(int); idle >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	c, err := p.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if p.hits.Load() < 1 {
		t.Fatalf("expected pool hit, stats=%v handshakes=%d", p.Stats(), n.Load())
	}
}
