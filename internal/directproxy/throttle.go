package directproxy

import (
	"net"
	"sync"
	"time"
)

// throttle is a token bucket shared by both directions of one relayed
// connection. feature.rate_limit_bytes_per_sec / chain.rate_limit_bps were
// stored and displayed but never applied to any byte, so a client could
// saturate the uplink regardless of the configured cap.
type throttle struct {
	mu     sync.Mutex
	bps    float64
	burst  float64
	tokens float64
	last   time.Time
}

func newThrottle(bps int64) *throttle {
	if bps <= 0 {
		return nil
	}
	b := float64(bps)
	// One second of burst keeps interactive traffic responsive while still
	// holding the long-run average at the configured rate.
	return &throttle{bps: b, burst: b, tokens: b, last: time.Now()}
}

// take blocks until n bytes may pass. n larger than the burst is allowed
// through after draining the bucket, so a single big write cannot deadlock.
func (t *throttle) take(n int) {
	if t == nil || n <= 0 {
		return
	}
	for {
		t.mu.Lock()
		now := time.Now()
		t.tokens += now.Sub(t.last).Seconds() * t.bps
		t.last = now
		if t.tokens > t.burst {
			t.tokens = t.burst
		}
		need := float64(n)
		if t.tokens >= need || need > t.burst {
			t.tokens -= need
			t.mu.Unlock()
			return
		}
		wait := time.Duration((need - t.tokens) / t.bps * float64(time.Second))
		t.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		if wait > time.Second {
			wait = time.Second
		}
		time.Sleep(wait)
	}
}

type throttledConn struct {
	net.Conn
	t *throttle
}

func (c *throttledConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.t.take(n)
	}
	return n, err
}

func (c *throttledConn) Write(p []byte) (int, error) {
	c.t.take(len(p))
	return c.Conn.Write(p)
}

// throttled wraps conn when a limit is configured; both directions of a relay
// must share one throttle for the cap to mean "per connection".
func throttled(conn net.Conn, t *throttle) net.Conn {
	if t == nil || conn == nil {
		return conn
	}
	return &throttledConn{Conn: conn, t: t}
}
