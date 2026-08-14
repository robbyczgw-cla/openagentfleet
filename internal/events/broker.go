package events

import (
	"context"
	"sync"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// Broker is an in-process fan-out channel for live clients. Durable events
// are written to SQLite before they are published; transient provider output
// can be published without being persisted.
type Broker struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]chan domain.StreamEvent
}

func New() *Broker {
	return &Broker{subscribers: make(map[uint64]chan domain.StreamEvent)}
}

// Subscribe returns a buffered channel and an idempotent unsubscribe function.
// The context is also observed so HTTP clients do not leave subscriptions
// behind when they disconnect.
func (b *Broker) Subscribe(ctx context.Context) (<-chan domain.StreamEvent, func()) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	channel := make(chan domain.StreamEvent, 128)
	b.subscribers[id] = channel
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if current, ok := b.subscribers[id]; ok {
				delete(b.subscribers, id)
				close(current)
			}
			b.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return channel, unsubscribe
}

// Publish never blocks the daemon. A slow remote client can reconnect and
// reload durable state from the bootstrap endpoint instead of backpressuring
// provider execution.
func (b *Broker) Publish(event domain.StreamEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, channel := range b.subscribers {
		select {
		case channel <- event:
		default:
		}
	}
}
