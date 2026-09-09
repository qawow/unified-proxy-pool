package directproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

// startForwardOnlyProxy runs a proxy that answers absolute-URL GET (the classic
// forward-proxy path) but rejects every CONNECT with 400.
//
// This is not hypothetical: 218.252.100.222:80 in the pool snapshot behaves
// exactly like this — `GET http://ipwho.is/` returns 200, every CONNECT returns
// 400. The Go validator records such a proxy as reachable (its HTTPS canary is
// what would reject it, and a caller-reported success never goes through that
// canary), so the only thing standing between it and a chain is whether chain
// selection checks for CONNECT support.
func startForwardOnlyProxy(t *testing.T) (addr string, connectAttempts *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var attempts atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.Method == http.MethodConnect {
					attempts.Add(1)
					_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
					return
				}
				if req.URL == nil || req.URL.Host == "" {
					_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
					return
				}
				upstream, err := net.DialTimeout("tcp", hostWithPort(req.URL.Host), 3*time.Second)
				if err != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
					return
				}
				defer upstream.Close()
				outReq := &http.Request{
					Method: req.Method,
					URL:    req.URL,
					Header: req.Header.Clone(),
					Host:   req.URL.Host,
				}
				outReq.Header.Del("Proxy-Connection")
				if err := outReq.Write(upstream); err != nil {
					return
				}
				_, _ = io.Copy(c, upstream)
			}(conn)
		}
	}()
	return ln.Addr().String(), &attempts
}

func hostWithPort(h string) string {
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h
	}
	return net.JoinHostPort(h, "80")
}

// A forward-only proxy must not be usable as a chain hop: dialProxyChainPool
// speaks CONNECT to every hop, even when the final request is plain HTTP.
//
// Today the pool can put such a proxy into a chain: POST /api/channels/report
// accepts a caller's verdict, and a caller that fetched an http:// URL only
// exercised forward GET, yet MarkValidated(addr, _, true) then marks the proxy
// validated/capped and moves it into keyChecked where chain picks sample from.
// Nothing between that report and dialProxyChainPool asks whether the proxy
// does CONNECT.
func TestForwardOnlyProxyRejectedAsChainHop(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()

	fwdAddr, connectAttempts := startForwardOnlyProxy(t)
	host, portStr, err := net.SplitHostPort(fwdAddr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	hop := freproxies.Proxy{Host: host, Port: port, Addr: fwdAddr, Protocol: "http"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialProxyChainPool(ctx, []freproxies.Proxy{hop}, strings.TrimPrefix(origin.URL, "http://"), nil)
	if err == nil {
		conn.Close()
		t.Fatal("dialProxyChainPool succeeded through a proxy that 400s every CONNECT")
	}
	if n := connectAttempts.Load(); n == 0 {
		t.Fatal("chain dialling never issued CONNECT to the hop; the premises of this test are wrong")
	}
	t.Logf("confirmed: chain requires CONNECT; forward-only hop rejected it (attempts=%d, err=%v)",
		connectAttempts.Load(), err)
}
