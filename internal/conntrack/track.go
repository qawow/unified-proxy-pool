package conntrack

import (
	"sync"
	"sync/atomic"
	"time"
)

type Conn struct {
	ID        int64     `json:"id"`
	Channel   string    `json:"channel"`
	ClientIP  string    `json:"client_ip"`
	Upstream  string    `json:"upstream,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpBytes   int64     `json:"up_bytes"`
	DownBytes int64     `json:"down_bytes"`
}

type Tracker struct {
	mu   sync.RWMutex
	seq  atomic.Int64
	live map[int64]*Conn
}

func New() *Tracker {
	return &Tracker{live: map[int64]*Conn{}}
}

var Default = New()

func (t *Tracker) Begin(channel, clientIP string) int64 {
	id := t.seq.Add(1)
	t.mu.Lock()
	t.live[id] = &Conn{ID: id, Channel: channel, ClientIP: clientIP, StartedAt: time.Now().UTC()}
	t.mu.Unlock()
	return id
}

func (t *Tracker) SetUpstream(id int64, upstream string) {
	t.mu.Lock()
	if c, ok := t.live[id]; ok {
		c.Upstream = upstream
	}
	t.mu.Unlock()
}

func (t *Tracker) End(id int64, up, down int64) {
	t.mu.Lock()
	delete(t.live, id)
	t.mu.Unlock()
	_ = up
	_ = down
}

func (t *Tracker) List() []Conn {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Conn, 0, len(t.live))
	for _, c := range t.live {
		out = append(out, *c)
	}
	return out
}
