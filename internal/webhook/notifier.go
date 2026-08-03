package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Notifier struct {
	mu       sync.Mutex
	url      string
	events   map[string]bool
	lastSent map[string]time.Time
	cooldown time.Duration
	client   *http.Client
}

func New() *Notifier {
	return &Notifier{
		events:   map[string]bool{},
		lastSent: map[string]time.Time{},
		cooldown: 10 * time.Minute,
		client:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (n *Notifier) Configure(url string, events []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.url = strings.TrimSpace(url)
	n.events = map[string]bool{}
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e != "" {
			n.events[e] = true
		}
	}
}

func (n *Notifier) Notify(event string, payload map[string]any) {
	n.mu.Lock()
	url := n.url
	ok := n.events[event] || n.events["*"]
	if url == "" || !ok {
		n.mu.Unlock()
		return
	}
	if t, exists := n.lastSent[event]; exists && time.Since(t) < n.cooldown {
		n.mu.Unlock()
		return
	}
	n.lastSent[event] = time.Now()
	n.mu.Unlock()

	body := map[string]any{
		"event":     event,
		"ts":        time.Now().UTC().Format(time.RFC3339),
		"payload":   payload,
		"service":   "unified-proxy-pool",
	}
	raw, _ := json.Marshal(body)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.client.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

var Default = New()
