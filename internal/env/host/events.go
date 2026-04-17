package host

import (
	"context"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
)

// eventHub fans a single producer (the host adapter) out to multiple
// subscribers via bounded per-subscriber channels. On overflow, the hub
// drops-oldest by non-blocking send, incrementing a drop counter via the
// supplied callback.
type eventHub struct {
	mu        sync.Mutex
	subs      []chan env.Event
	closed    bool
	onDrop    func()
	lastEvent time.Time
}

func newEventHub(onDrop func()) *eventHub {
	return &eventHub{onDrop: onDrop}
}

func (h *eventHub) subscribe(ctx context.Context) <-chan env.Event {
	ch := make(chan env.Event, env.EventChannelCapacity)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch
	}
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		h.unsubscribe(ch)
	}()
	return ch
}

func (h *eventHub) unsubscribe(ch chan env.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.subs {
		if s == ch {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			close(s)
			return
		}
	}
}

func (h *eventHub) emit(evt env.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.lastEvent = evt.Timestamp
	for _, ch := range h.subs {
		select {
		case ch <- evt:
		default:
			if h.onDrop != nil {
				h.onDrop()
			}
		}
	}
}

func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, ch := range h.subs {
		close(ch)
	}
	h.subs = nil
}

func (h *eventHub) lastEventAt() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastEvent
}
