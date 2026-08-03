package traffic

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type Snapshot struct {
	UpBytes     int64            `json:"up_bytes"`
	DownBytes   int64            `json:"down_bytes"`
	Connections int64            `json:"connections"`
	Success     int64            `json:"success"`
	Fail        int64            `json:"fail"`
	ByChannel   map[string]*Chan `json:"by_channel"`
	ActiveConns int64            `json:"active_conns"`
	ActiveIn    int64            `json:"active_in"`
	ActiveOut   int64            `json:"active_out"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type Chan struct {
	UpBytes     int64 `json:"up_bytes"`
	DownBytes   int64 `json:"down_bytes"`
	Connections int64 `json:"connections"`
	Success     int64 `json:"success"`
	Fail        int64 `json:"fail"`
	ActiveIn    int64 `json:"active_in"`
	ActiveOut   int64 `json:"active_out"`
}

type Counter struct {
	up, down, conns, ok, fail, active atomic.Int64
	activeIn, activeOut               atomic.Int64
	mu                                sync.Mutex
	channels                          map[string]*chanCounters
}

type chanCounters struct {
	up, down, conns, ok, fail, activeIn, activeOut atomic.Int64
}

func New() *Counter {
	return &Counter{channels: map[string]*chanCounters{}}
}

func (c *Counter) channel(name string) *chanCounters {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.channels[name]
	if !ok {
		ch = &chanCounters{}
		c.channels[name] = ch
	}
	return ch
}

// BeginInbound: client connected to our listener.
func (c *Counter) BeginInbound(channel string) {
	c.conns.Add(1)
	c.active.Add(1)
	c.activeIn.Add(1)
	ch := c.channel(channel)
	ch.conns.Add(1)
	ch.activeIn.Add(1)
}

// BeginOutbound: upstream tunnel established.
func (c *Counter) BeginOutbound(channel string) {
	c.activeOut.Add(1)
	c.channel(channel).activeOut.Add(1)
}

// EndConn finishes a session.
func (c *Counter) EndConn(channel string, success bool, up, down int64, hadOutbound bool) {
	if c.active.Load() > 0 {
		c.active.Add(-1)
	}
	if c.activeIn.Load() > 0 {
		c.activeIn.Add(-1)
	}
	ch := c.channel(channel)
	if ch.activeIn.Load() > 0 {
		ch.activeIn.Add(-1)
	}
	if hadOutbound {
		if c.activeOut.Load() > 0 {
			c.activeOut.Add(-1)
		}
		if ch.activeOut.Load() > 0 {
			ch.activeOut.Add(-1)
		}
	}
	if up > 0 {
		c.up.Add(up)
		ch.up.Add(up)
	}
	if down > 0 {
		c.down.Add(down)
		ch.down.Add(down)
	}
	if success {
		c.ok.Add(1)
		ch.ok.Add(1)
	} else {
		c.fail.Add(1)
		ch.fail.Add(1)
	}
}

// AddConn keeps backward compatible API = inbound begin only.
// Prefer BeginInbound + BeginOutbound + EndConn(..., hadOutbound).
func (c *Counter) AddConn(channel string) {
	c.BeginInbound(channel)
}

func (c *Counter) Snapshot() Snapshot {
	c.mu.Lock()
	by := make(map[string]*Chan, len(c.channels))
	for k, v := range c.channels {
		by[k] = &Chan{
			UpBytes:     v.up.Load(),
			DownBytes:   v.down.Load(),
			Connections: v.conns.Load(),
			Success:     v.ok.Load(),
			Fail:        v.fail.Load(),
			ActiveIn:    v.activeIn.Load(),
			ActiveOut:   v.activeOut.Load(),
		}
	}
	c.mu.Unlock()
	return Snapshot{
		UpBytes:     c.up.Load(),
		DownBytes:   c.down.Load(),
		Connections: c.conns.Load(),
		Success:     c.ok.Load(),
		Fail:        c.fail.Load(),
		ByChannel:   by,
		ActiveConns: c.active.Load(),
		ActiveIn:    c.activeIn.Load(),
		ActiveOut:   c.activeOut.Load(),
		UpdatedAt:   time.Now().UTC(),
	}
}

type CountingWriter struct {
	W io.Writer
	N *int64
}

func (c *CountingWriter) Write(p []byte) (int, error) {
	n, err := c.W.Write(p)
	if n > 0 && c.N != nil {
		atomic.AddInt64(c.N, int64(n))
	}
	return n, err
}

func BidirectionalRelay(client, upstream io.ReadWriteCloser) (up, down int64, err error) {
	var upN, downN int64
	errCh := make(chan error, 2)
	go func() {
		_, e := io.Copy(&CountingWriter{W: upstream, N: &upN}, client)
		errCh <- e
	}()
	go func() {
		_, e := io.Copy(&CountingWriter{W: client, N: &downN}, upstream)
		errCh <- e
	}()
	e1 := <-errCh
	_ = client.Close()
	_ = upstream.Close()
	e2 := <-errCh
	up, down = upN, downN
	if e1 != nil && e1 != io.EOF {
		return up, down, e1
	}
	if e2 != nil && e2 != io.EOF {
		return up, down, e2
	}
	return up, down, nil
}

var Default = New()

func Get(_ context.Context) Snapshot {
	return Default.Snapshot()
}
