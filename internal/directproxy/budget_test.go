package directproxy

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

// TestSingleHopBudgetIsPerAttempt reproduces the class of bug the chain path
// already documents at its own dial loop: a dialCtx shared across failover
// candidates lets one slow proxy eat the whole budget, and every later
// candidate then fails instantly with deadline-exceeded — and was scored as
// a dead proxy for a budget exhaustion that was never its failure.
//
// dialViaWithFailoverClient used to share one context across all eight
// candidates. This test drives that loop with two real TCP upstreams — the
// first deliberately slow past the budget, the second fast — and asserts the
// second still receives a usable budget rather than the leftovers of the first.
func TestSingleHopBudgetIsPerAttempt(t *testing.T) {
	slow := newBudgetProxy(func(c net.Conn) {
		time.Sleep(1500 * time.Millisecond)
		_ = c.Close()
	})
	fast := newBudgetProxy(func(c net.Conn) {
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		time.Sleep(3 * time.Second)
		_ = c.Close()
	})
	defer slow.Close()
	defer fast.Close()

	store := freproxies.NewMemoryStore()
	ctx := context.Background()
	for _, p := range []freproxies.Proxy{
		{Host: "127.0.0.1", Port: slow.Port(), Protocol: "http", Source: "test", Score: 90},
		{Host: "127.0.0.1", Port: fast.Port(), Protocol: "http", Source: "test", Score: 95},
	} {
		_, _ = store.AddRaw(ctx, []freproxies.Proxy{p})
	}
	// MarkValidated needs the address AddRaw produced, which is Host:Port.
	for _, port := range []int{slow.Port(), fast.Port()} {
		_ = store.MarkValidated(ctx, "127.0.0.1:"+strconv.Itoa(port), 20, true)
	}
	free := freproxies.NewService(store, nil, nil, false)

	// Total budget smaller than the slow proxy's stall, but far larger than the
	// fast proxy needs. Shared budget: the slow one exhausts it and the fast
	// one is handed an already-expired context. Per-attempt: the slow one times
	// out on its own clock, the fast one connects.
	s := &Server{chainOpts: DefaultChainOptions()}
	s.chainOpts.DialTimeoutMS = 700
	s.free = free

	dialCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	conn, _, err := s.dialViaWithFailoverClient(dialCtx, "example.com:80", "")
	if err != nil {
		t.Fatalf("dialViaWithFailoverClient: %v", err)
	}
	defer conn.Close()

	if fast.served.Load() == 0 {
		t.Fatal("fast upstream never got a usable budget; the slow one consumed the shared dialCtx")
	}
}

// budgetProxy is a TCP listener that counts accepted connections and handler
// completions, so the test can prove which upstream was actually given a
// chance to connect.
type budgetProxy struct {
	ln        net.Listener
	served    atomic.Int64
	completed atomic.Int64
	handler   func(net.Conn)
	done      chan struct{}
}

func newBudgetProxy(handler func(net.Conn)) *budgetProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	p := &budgetProxy{ln: ln, handler: handler, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.served.Add(1)
			go func() {
				defer p.completed.Add(1)
				p.handler(c)
			}()
		}
	}()
	return p
}

func (p *budgetProxy) Addr() string { return p.ln.Addr().String() }
func (p *budgetProxy) Port() int {
	host, port, _ := net.SplitHostPort(p.ln.Addr().String())
	_ = host
	pr := 0
	for i := 0; i < len(port); i++ {
		pr = pr*10 + int(port[i]-'0')
	}
	return pr
}

func (p *budgetProxy) Close() error {
	err := p.ln.Close()
	select {
	case <-p.done:
	case <-time.After(time.Second):
	}
	return err
}
