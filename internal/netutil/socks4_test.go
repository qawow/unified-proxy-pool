package netutil

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeSOCKS4 accepts one connection, checks the request, and replies granted.
func fakeSOCKS4(t *testing.T, wantHost string, wantPort int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		head := make([]byte, 8)
		if _, err := io.ReadFull(conn, head); err != nil {
			return
		}
		if head[0] != 0x04 || head[1] != 0x01 {
			return
		}
		if got := int(binary.BigEndian.Uint16(head[2:4])); got != wantPort {
			t.Errorf("port = %d, want %d", got, wantPort)
		}
		// USERID (NUL-terminated), then hostname for SOCKS4a.
		readCString(conn)
		if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
			if got := readCString(conn); got != wantHost {
				t.Errorf("socks4a hostname = %q, want %q", got, wantHost)
			}
		}
		_, _ = conn.Write([]byte{0x00, 0x5a, 0, 0, 0, 0, 0, 0})
		_, _ = io.Copy(io.Discard, conn)
	}()
	return ln
}

func readCString(r io.Reader) string {
	var out []byte
	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return string(out)
		}
		if buf[0] == 0 {
			return string(out)
		}
		out = append(out, buf[0])
	}
}

func TestDialSOCKS4Hostname(t *testing.T) {
	ln := fakeSOCKS4(t, "example.com", 443)
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialSOCKS4(ctx, &net.Dialer{}, ln.Addr().String(), "example.com:443")
	if err != nil {
		t.Fatalf("DialSOCKS4: %v", err)
	}
	conn.Close()
}

func TestDialSOCKS4IPv4Literal(t *testing.T) {
	ln := fakeSOCKS4(t, "", 8080)
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialSOCKS4(ctx, &net.Dialer{}, ln.Addr().String(), "1.2.3.4:8080")
	if err != nil {
		t.Fatalf("DialSOCKS4: %v", err)
	}
	conn.Close()
}

func TestDialSOCKS4RejectsIPv6(t *testing.T) {
	ctx := context.Background()
	if _, err := DialSOCKS4(ctx, &net.Dialer{}, "127.0.0.1:1", "[2001:db8::1]:443"); err == nil {
		t.Fatal("expected an error for an IPv6 target")
	}
}

func TestIsSOCKS4(t *testing.T) {
	for _, p := range []string{"socks4", "SOCKS4", " socks4a "} {
		if !IsSOCKS4(p) {
			t.Errorf("IsSOCKS4(%q) = false", p)
		}
	}
	for _, p := range []string{"socks5", "http", ""} {
		if IsSOCKS4(p) {
			t.Errorf("IsSOCKS4(%q) = true", p)
		}
	}
}
