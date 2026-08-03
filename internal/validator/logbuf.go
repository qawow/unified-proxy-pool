package validator

import (
	"sync"
	"time"
)

type LogLine struct {
	At        time.Time `json:"at"`
	Level     string    `json:"level"`
	Addr      string    `json:"addr,omitempty"`
	Message   string    `json:"message"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Source    string    `json:"source,omitempty"`
}

type LogBuffer struct {
	mu      sync.RWMutex
	lines   []LogLine
	cap     int
	running bool
}

func NewLogBuffer(cap int) *LogBuffer {
	if cap < 50 {
		cap = 200
	}
	return &LogBuffer{cap: cap, lines: make([]LogLine, 0, cap)}
}

var DefaultLogs = NewLogBuffer(250)

func (b *LogBuffer) SetRunning(v bool) {
	b.mu.Lock()
	b.running = v
	b.mu.Unlock()
}

func (b *LogBuffer) Running() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

func (b *LogBuffer) Add(level, addr, msg, source string, latency int64) {
	line := LogLine{
		At:        time.Now().UTC(),
		Level:     level,
		Addr:      addr,
		Message:   msg,
		LatencyMS: latency,
		Source:    source,
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.cap {
		b.lines = b.lines[len(b.lines)-b.cap:]
	}
}

func (b *LogBuffer) List(limit int) []LogLine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > len(b.lines) {
		limit = len(b.lines)
	}
	if limit == 0 {
		return []LogLine{}
	}
	start := len(b.lines) - limit
	out := make([]LogLine, limit)
	copy(out, b.lines[start:])
	return out
}

func (b *LogBuffer) Clear() {
	b.mu.Lock()
	b.lines = b.lines[:0]
	b.mu.Unlock()
}
