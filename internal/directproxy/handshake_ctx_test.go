package directproxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// silentProxy reads one request line, signals gotReq, then holds the conn open
// without ever answering — the shape that used to pin a dial attempt for the
// full fixed handshake deadline.
func silentProxy(t *testing.T) (addr string, gotReq chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	gotReq = make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				if _, err := c.Read(buf); err != nil {
					return
				}
				gotReq <- struct{}{}
				_, _ = c.Read(buf) // silent until the peer hangs up
			}(c)
		}
	}()
	return ln.Addr().String(), gotReq
}

func dialTo(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// A cancelled attempt ctx must abort a silent CONNECT handshake quickly —
// previously the fixed 12s deadline kept waiting and a late 200 could even
// hand a dead tunnel back to the caller.
func TestHTTPConnectHonoursAttemptCancel(t *testing.T) {
	addr, gotReq := silentProxy(t)
	conn := dialTo(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-gotReq
		cancel()
	}()
	start := time.Now()
	nc, err := httpConnectOver(ctx, conn, "example.test:443", "", "")
	if nc != nil {
		nc.Close()
	}
	if err == nil {
		t.Fatal("silent CONNECT must not succeed after cancel")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("cancel took %v to interrupt CONNECT", el)
	}
}

// A late 200 arriving after cancel must not turn into a successful tunnel.
func TestLateConnectResponseAfterCancelIsNotSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotReq := make(chan struct{}, 1)
	respond := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 512)
		_, _ = c.Read(buf)
		gotReq <- struct{}{}
		<-respond
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	}()
	conn := dialTo(t, ln.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		nc, err := httpConnectOver(ctx, conn, "example.test:443", "", "")
		if nc != nil {
			nc.Close()
		}
		done <- err
	}()
	<-gotReq
	cancel()
	close(respond) // the 200 arrives after the attempt was cancelled
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a 200 after cancel must not produce a tunnel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handshake did not finish after cancel")
	}
}

// A ctx deadline shorter than the handshake budget must win over the fixed
// 12s ceiling.
func TestHandshakeUsesAttemptDeadline(t *testing.T) {
	addr, _ := silentProxy(t)
	conn := dialTo(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	nc, err := httpConnectOver(ctx, conn, "example.test:443", "", "")
	if nc != nil {
		nc.Close()
	}
	if err == nil {
		t.Fatal("silent CONNECT must fail under the attempt deadline")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("attempt deadline did not bound the handshake: %v", el)
	}
}

// The same binding must cover the SOCKS5 CONNECT command path.
func TestSocks5ConnectHonoursAttemptCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotReq := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Answer the method greeting, then go silent on the CONNECT request.
		buf := make([]byte, 8)
		if _, err := io.ReadFull(c, buf[:2]); err != nil {
			return
		}
		_, _ = c.Write([]byte{0x05, 0x00})
		if _, err := c.Read(buf); err != nil {
			return
		}
		gotReq <- struct{}{}
		_, _ = c.Read(buf)
	}()
	conn := dialTo(t, ln.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-gotReq
		cancel()
	}()
	start := time.Now()
	nc, err := socks5ConnectCmd(ctx, conn, "example.test:443")
	if nc != nil {
		nc.Close()
	}
	if err == nil {
		t.Fatal("silent SOCKS5 CONNECT must not succeed after cancel")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("cancel took %v to interrupt SOCKS5 CONNECT", el)
	}
}
