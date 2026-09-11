package directproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"unified-proxy-pool/internal/crawlers"
	"unified-proxy-pool/internal/freproxies"
)

// recordingCONNECTProxy is startCONNECTProxy plus a log of the CONNECT targets
// it was asked for, so tests can prove a probe really transited the node.
func startRecordingCONNECTProxy(t *testing.T) (addr string, seen func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var targets []string
	seen = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), targets...)
	}

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
				mu.Lock()
				targets = append(targets, req.Host)
				mu.Unlock()
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
	return ln.Addr().String(), seen
}

func TestProbeFrontUnset(t *testing.T) {
	s := &Server{}
	if got := s.ProbeFront(); got != nil {
		t.Fatalf("ProbeFront = %+v, want nil with no exit_via", got)
	}
}

func TestProbeFrontBrokenConfig(t *testing.T) {
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "https://203.0.113.9:8443"})
	f := s.ProbeFront()
	if f == nil || f.Err == nil {
		t.Fatalf("ProbeFront = %+v, want a fail-closed error for an https:// front", f)
	}
}

func TestProbeFrontModes(t *testing.T) {
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "socks5://127.0.0.1:1080"})
	t.Cleanup(func() { s.getViaPool().Close() })
	f := s.ProbeFront()
	if f == nil || f.Err != nil || f.Hop.Addr != "127.0.0.1:1080" {
		t.Fatalf("ProbeFront = %+v", f)
	}
	if f.Mode != "" {
		t.Fatalf("mode = %q, want empty (entry default)", f.Mode)
	}
	s.SetChainOptions(ChainOptions{ExitVia: "socks5://127.0.0.1:1080", ExitViaMode: "exit"})
	if got := s.ProbeFront(); got == nil || got.Mode != "exit" {
		t.Fatalf("ProbeFront = %+v, want mode exit", got)
	}
}

// Entry mode, front dead at TCP level: the failure is the front's.
func TestChainProbeDialEntryFrontDown(t *testing.T) {
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "http://127.0.0.1:1"}) // refused fast
	t.Cleanup(func() { s.getViaPool().Close() })
	via, err := ParseViaProxy("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	cand := freproxies.Proxy{Addr: "127.0.0.1:2", Protocol: "http"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := s.ChainProbeDial(ctx, []freproxies.Proxy{via, cand}, "example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded against a dead front")
	}
	if !errors.Is(err, freproxies.ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable", err)
	}
}

// Exit mode, front dead: the healthy candidate is asked to CONNECT to the dead
// VPS and answers 502. Naive attribution bills that to the candidate — the
// liveness re-check must hand it back to the front.
func TestChainProbeDialExitFrontDown(t *testing.T) {
	candAddr, _ := startRecordingCONNECTProxy(t)
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "http://127.0.0.1:1", ExitViaMode: "exit"})
	via, err := ParseViaProxy("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	cand := freproxies.Proxy{Addr: candAddr, Protocol: "http"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := s.ChainProbeDial(ctx, []freproxies.Proxy{cand, via}, "example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded against a dead front")
	}
	if !errors.Is(err, freproxies.ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable (the candidate is healthy)", err)
	}
}

// Contrast: front alive, candidate unreachable through it. The failure belongs
// to the candidate and must not be wrapped as a front failure.
func TestChainProbeDialCandidateUnreachableNotFrontFault(t *testing.T) {
	frontAddr, _ := startRecordingCONNECTProxy(t)
	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "http://" + frontAddr})
	t.Cleanup(func() { s.getViaPool().Close() })
	via, err := ParseViaProxy("http://" + frontAddr)
	if err != nil {
		t.Fatal(err)
	}
	cand := freproxies.Proxy{Addr: "127.0.0.1:1", Protocol: "http"} // nothing there

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := s.ChainProbeDial(ctx, []freproxies.Proxy{via, cand}, "example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("dial succeeded through a dead candidate")
	}
	if errors.Is(err, freproxies.ErrFrontUnavailable) {
		t.Fatalf("candidate failure was billed to the healthy front: %v", err)
	}
}

// Happy path: the probe chain really runs front → candidate → origin, and bytes
// flow back.
func TestChainProbeDialSuccessThroughFront(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	frontAddr, frontSeen := startRecordingCONNECTProxy(t)
	candAddr, candSeen := startRecordingCONNECTProxy(t)

	s := &Server{}
	s.SetChainOptions(ChainOptions{ExitVia: "http://" + frontAddr})
	t.Cleanup(func() { s.getViaPool().Close() })
	via, err := ParseViaProxy("http://" + frontAddr)
	if err != nil {
		t.Fatal(err)
	}
	cand := freproxies.Proxy{Addr: candAddr, Protocol: "http"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := s.ChainProbeDial(ctx, []freproxies.Proxy{via, cand}, originAddr)
	if err != nil {
		t.Fatalf("ChainProbeDial: %v", err)
	}
	defer conn.Close()

	// Prove the tunnel carries traffic: issue a request to the origin over it.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+originAddr+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write over tunnel: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read over tunnel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("origin status over tunnel = %d", resp.StatusCode)
	}

	if got := frontSeen(); len(got) == 0 || got[len(got)-1] != candAddr {
		t.Fatalf("front CONNECT log = %v, want it asked for the candidate %s", got, candAddr)
	}
	if got := candSeen(); len(got) == 0 || got[len(got)-1] != originAddr {
		t.Fatalf("candidate CONNECT log = %v, want it asked for the origin %s", got, originAddr)
	}
}

// The app wiring itself: with the server as the probe-front provider, a broken
// exit_via fails validation closed without touching the pool.
func TestProbeFrontWiringFailClosed(t *testing.T) {
	free := freproxies.NewService(freproxies.NewMemoryStore(), crawlers.NewRegistry(nil), nil, false)
	s := New(Config{}, free)
	s.SetChainOptions(ChainOptions{ExitVia: "https://203.0.113.9:8443"}) // unsupported scheme
	free.SetProbeFront(s.ProbeFront, s.ChainProbeDial)

	ctx := context.Background()
	if _, err := free.Store().AddRaw(ctx, []freproxies.Proxy{{Host: "10.8.8.8", Port: 8080, Protocol: "http", Source: "test"}}); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	_, err := free.TestProxyURLs(ctx, "10.8.8.8:8080", []string{"http://example.com/"}, time.Second, false)
	if !errors.Is(err, freproxies.ErrFrontUnavailable) {
		t.Fatalf("err = %v, want ErrFrontUnavailable", err)
	}
	stored, gerr := free.Store().Get(ctx, "10.8.8.8:8080")
	if gerr != nil || !stored.LastCheck.IsZero() {
		t.Fatalf("broken front was scored against the candidate: %+v (get err %v)", stored, gerr)
	}
}
