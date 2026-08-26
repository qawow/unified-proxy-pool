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
	"testing"
	"time"

	"unified-proxy-pool/internal/chanpolicy"
	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// startCONNECTProxy runs a minimal CONNECT proxy, which is what dialProxyChain
// expects of an upstream even for plain-HTTP requests.
func startCONNECTProxy(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

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
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				upstream, err := net.DialTimeout("tcp", req.Host, 3*time.Second)
				if err != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer upstream.Close()
				if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
					return
				}
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, br); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, upstream); done <- struct{}{} }()
				<-done
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// newServerWithPool builds a DirectProxy whose free pool contains exactly the
// given upstream addresses.
func newServerWithPool(t *testing.T, addrs ...string) (*Server, *freproxies.Service) {
	t.Helper()
	free := freproxies.NewService(freproxies.NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	ctx := context.Background()
	var proxies []freproxies.Proxy
	for _, addr := range addrs {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("SplitHostPort(%s): %v", addr, err)
		}
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			t.Fatalf("parse port %s: %v", portStr, err)
		}
		proxies = append(proxies, freproxies.Proxy{Host: host, Port: port, Addr: addr, Protocol: "http"})
	}
	if _, err := free.Store().AddRaw(ctx, proxies); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	for _, p := range proxies {
		if err := free.Store().MarkValidated(ctx, p.Addr, 10, true); err != nil {
			t.Fatalf("MarkValidated: %v", err)
		}
	}
	s := New(Config{Enabled: false, ChainEnabled: false}, free)
	return s, free
}

// doProxiedGET drives handleHTTP over an in-memory pipe and returns the response
// the client saw.
func doProxiedGET(t *testing.T, s *Server, targetURL string) *http.Response {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()

	errCh := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		finished := false
		errCh <- s.handleHTTP(context.Background(), serverSide, bufio.NewReader(serverSide), false, &finished)
	}()

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		targetURL, strings.TrimPrefix(strings.TrimPrefix(targetURL, "http://"), "https://"))
	if _, err := io.WriteString(clientSide, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(clientSide), nil)
	if err != nil {
		t.Fatalf("read response: %v (handler err: %v)", err, <-errCh)
	}
	return resp
}

// The automatic side of the feature: a plain-HTTP 403 is visible to the pool, so
// it must ban the exit proxy for that destination without anyone reporting it.
func TestPlainHTTPForbiddenBansExitProxyForChannel(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer origin.Close()

	upstreamAddr := startCONNECTProxy(t)
	s, _ := newServerWithPool(t, upstreamAddr)

	clk := &fakeNow{t: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)}
	registry := chanpolicy.New(chanpolicy.Options{Policy: chanpolicy.Defaults(), Now: clk.now})
	s.SetChannelPolicy(registry)

	resp := doProxiedGET(t, s, origin.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status = %d, want 403 (the fixture is not exercising the path)", resp.StatusCode)
	}

	channel := registry.ChannelFor(strings.TrimPrefix(origin.URL, "http://"))
	if channel == "" {
		t.Fatal("no channel derived for the origin")
	}
	if !registry.Banned(channel, upstreamAddr) {
		t.Errorf("403 through %s did not ban it for channel %s; bans=%+v",
			upstreamAddr, channel, registry.Bans(channel))
	}
	// The same proxy must remain usable for a different destination.
	if registry.Banned("example.org", upstreamAddr) {
		t.Error("the ban leaked to another channel")
	}
}

// A 200 must leave the proxy in rotation.
func TestPlainHTTPSuccessDoesNotBan(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()

	upstreamAddr := startCONNECTProxy(t)
	s, _ := newServerWithPool(t, upstreamAddr)

	clk := &fakeNow{t: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)}
	registry := chanpolicy.New(chanpolicy.Options{Policy: chanpolicy.Defaults(), Now: clk.now})
	s.SetChannelPolicy(registry)

	resp := doProxiedGET(t, s, origin.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	channel := registry.ChannelFor(strings.TrimPrefix(origin.URL, "http://"))
	if registry.Banned(channel, upstreamAddr) {
		t.Error("a successful request produced a ban")
	}
}

// When every upstream is dead, the failures are attributed to the destination so
// repeated attempts stop reusing the same broken proxies for it.
func TestDialFailureIsRecordedAgainstChannel(t *testing.T) {
	// Port 1 on loopback refuses immediately.
	s, _ := newServerWithPool(t, "127.0.0.1:1")
	pol := newRecordingPolicy("taobao.com")
	s.SetChannelPolicy(pol)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := s.openUpstream(ctx, "item.taobao.com:80", false); err == nil {
		t.Fatal("expected the dial to fail against a refused port")
	}
	if len(pol.outcomes) == 0 {
		t.Fatal("dial failure was not recorded against the channel")
	}
	for _, o := range pol.outcomes {
		if o.OK {
			t.Errorf("recorded a success for a failed dial: %+v", o)
		}
		if o.Channel != "taobao.com" {
			t.Errorf("outcome filed against channel %q, want taobao.com", o.Channel)
		}
		if o.Err == "" {
			t.Error("no error tag recorded; timeout-vs-refused drives different rules")
		}
	}
}

type fakeNow struct{ t time.Time }

func (f *fakeNow) now() time.Time { return f.t }
