package directproxy

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unified-proxy-pool/internal/freproxies"
)

const (
	viaPoolMaxIdle = 8
	viaPoolWarm    = 4
	viaPoolIdleAge = 25 * time.Second
	viaDialTimeout = 4 * time.Second
	viaKeepAlive   = 20 * time.Second
)

// socksAuthed is a TCP connection that already finished SOCKS5 auth.
// tunnelThrough skips the handshake and only sends CONNECT.
type socksAuthed struct{ net.Conn }

type idleConn struct {
	c  net.Conn
	at time.Time
}

type viaPool struct {
	hop  freproxies.Proxy
	stop chan struct{}

	mu   sync.Mutex
	idle []idleConn
	dead bool

	hits   atomic.Int64
	misses atomic.Int64
	dials  atomic.Int64
	drops  atomic.Int64
}

func newViaPool(hop freproxies.Proxy) *viaPool {
	p := &viaPool{hop: hop, stop: make(chan struct{})}
	go p.loop()
	return p
}

func (p *viaPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead = true
	close(p.stop)
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, it := range idle {
		_ = it.c.Close()
	}
}

func (p *viaPool) Stats() map[string]any {
	if p == nil {
		return map[string]any{"enabled": false}
	}
	p.mu.Lock()
	n := len(p.idle)
	p.mu.Unlock()
	return map[string]any{
		"enabled": true,
		"addr":    p.hop.Addr,
		"proto":   p.hop.Protocol,
		"idle":    n,
		"hits":    p.hits.Load(),
		"misses":  p.misses.Load(),
		"dials":   p.dials.Load(),
		"drops":   p.drops.Load(),
	}
}

func (p *viaPool) Take(ctx context.Context) (net.Conn, error) {
	if p == nil {
		return nil, errNoViaPool
	}
	if c := p.pop(); c != nil {
		p.hits.Add(1)
		return c, nil
	}
	p.misses.Add(1)
	return p.dialReady(ctx)
}

func (p *viaPool) pop() net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for len(p.idle) > 0 {
		last := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if now.Sub(last.at) > viaPoolIdleAge {
			_ = last.c.Close()
			p.drops.Add(1)
			continue
		}
		return last.c
	}
	return nil
}

func (p *viaPool) put(c net.Conn) {
	if c == nil {
		return
	}
	p.mu.Lock()
	if p.dead || len(p.idle) >= viaPoolMaxIdle {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.idle = append(p.idle, idleConn{c: c, at: time.Now()})
	p.mu.Unlock()
}

func (p *viaPool) dialReady(ctx context.Context) (net.Conn, error) {
	p.dials.Add(1)
	raw, err := dialFast(ctx, p.hop.Addr)
	if err != nil {
		return nil, err
	}
	proto := strings.ToLower(p.hop.Protocol)
	if proto == "socks5" || proto == "socks" || proto == "socks4" {
		if err := socks5Handshake(raw, p.hop.Username, p.hop.Password); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return &socksAuthed{Conn: raw}, nil
	}
	return raw, nil
}

func (p *viaPool) loop() {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	p.warm()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.warm()
			p.expire()
		}
	}
}

func (p *viaPool) warm() {
	p.mu.Lock()
	need := viaPoolWarm - len(p.idle)
	dead := p.dead
	p.mu.Unlock()
	if dead || need <= 0 {
		return
	}
	for i := 0; i < need; i++ {
		c, err := p.dialReady(context.Background())
		if err != nil {
			return
		}
		p.put(c)
	}
}

func (p *viaPool) expire() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	kept := p.idle[:0]
	for _, it := range p.idle {
		if now.Sub(it.at) > viaPoolIdleAge {
			_ = it.c.Close()
			p.drops.Add(1)
			continue
		}
		kept = append(kept, it)
	}
	p.idle = kept
}

func dialFast(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   viaDialTimeout,
		KeepAlive: viaKeepAlive,
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(viaKeepAlive)
	}
	return conn, nil
}

func dialEntry(ctx context.Context, hop freproxies.Proxy, pool *viaPool) (net.Conn, error) {
	if pool != nil && hop.Source == "exit_via" {
		return pool.Take(ctx)
	}
	return dialFast(ctx, hop.Addr)
}

var errNoViaPool = errString("via pool disabled")

type errString string

func (e errString) Error() string { return string(e) }
