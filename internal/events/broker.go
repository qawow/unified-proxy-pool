package events

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}

type Broker struct {
	mu      sync.RWMutex
	nextID  int
	clients map[int]chan []byte
}

func NewBroker() *Broker {
	return &Broker{clients: make(map[int]chan []byte)}
}

func (b *Broker) Subscribe() (int, <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan []byte, 32)
	b.clients[id] = ch
	return id, ch
}

func (b *Broker) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.clients[id]; ok {
		delete(b.clients, id)
		close(ch)
	}
}

func (b *Broker) Publish(eventType string, payload interface{}) {
	data, err := json.Marshal(Event{
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.clients {
		select {
		case ch <- data:
		default:
		}
	}
}
