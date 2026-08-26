package probe

import (
	"context"
	"net"
	"testing"
	"time"

	"unified-proxy-pool/internal/models"
)

func TestIsNativeProtocol(t *testing.T) {
	if !isNativeProtocol("socks5") || !isNativeProtocol("HTTP") {
		t.Fatal("expected http/socks5 to be native")
	}
	if isNativeProtocol("vless") || isNativeProtocol("hysteria2") || isNativeProtocol("ss") {
		t.Fatal("tunnel protocols must not be native")
	}
}

func TestJoinHostPortIPv6(t *testing.T) {
	got := joinHostPort("2a13:9500:15c:0:cafe::2a", 19051)
	want := "[2a13:9500:15c:0:cafe::2a]:19051"
	if got != want {
		t.Fatalf("joinHostPort = %q, want %q", got, want)
	}
}

func TestTCPDialClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = tcpDialMS(ctx, "127.0.0.1", addr.Port, time.Second)
	if err == nil {
		t.Fatal("expected error dialing closed port")
	}
}

func TestCredsFromNormalizedJSON(t *testing.T) {
	c := credsFromNormalizedJSON(`{"type":"socks5","tls":true,"username":"ob"}`)
	if !c.TLS {
		t.Fatal("expected tls true")
	}
	c = credsFromNormalizedJSON(`{"type":"ss"}`)
	if c.TLS {
		t.Fatal("did not expect tls")
	}
}

func TestNativeHTTPAgainstClosedProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := probeNativeHTTP(ctx, models.RuntimeNode{
		Protocol: "http",
		Server:   "127.0.0.1",
		Port:     1,
	}, "http://example.invalid/", time.Second)
	if err == nil {
		t.Fatal("expected native probe to fail")
	}
}
