// Package publisher tails the events projection in SQLite by monotonic
// `seq` and fans new rows out to active SubscribeEvents subscribers.
// Replaces the previous in-memory ingestion publisher; the producer
// side is now the per-session tailer (see internal/tailer).
//
// Two key reliability properties (spec §3.6):
//
//  1. Close-on-overflow: if a subscriber's buffer fills, the publisher
//     CLOSES that subscriber's channel rather than silently dropping
//     events. The client then reconnects with `since_seq` and the
//     publisher replays from the events table.
//  2. Subscribe-then-snapshot: subscribers register *before* the
//     server reads the snapshot, so any event that lands in the gap
//     between snapshot read and stream-go-live still arrives.
package publisher

import (
	"context"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Subscriber represents one live SubscribeEvents stream. The publisher
// owns the channel; the consumer reads from Events. When the channel
// is closed without a corresponding Unsubscribe, the consumer must
// reconnect with `since_seq` to resume.
type Subscriber struct {
	ID    string
	Since int64 // last seq the client has seen; 0 means "send everything"

	events chan *gruv1.SessionEvent
	closed atomic.Bool
}

// Events returns the receive-side channel. The channel is closed when
// the publisher decides to drop this subscriber (overflow, shutdown).
// Callers should treat a closed channel as "reconnect with since_seq".
func (s *Subscriber) Events() <-chan *gruv1.SessionEvent { return s.events }

// IsClosed reports whether the publisher has dropped this subscriber.
func (s *Subscriber) IsClosed() bool { return s.closed.Load() }

// Publisher tails the events table by `seq` and fans rows out to all
// subscribed streams. Use NewPublisher to construct one.
type Publisher struct {
	store      *store.Store
	bufferSize int
	logger     *log.Logger

	mu          sync.Mutex
	subscribers map[string]*Subscriber

	wakeup chan struct{}
}

// NewPublisher returns a publisher that tails the given store. Call
// Run to start the fan-out loop. The buffer-size knob controls when a
// subscriber gets dropped on overflow; 256 has been comfortable for a
// small fleet.
func NewPublisher(s *store.Store) *Publisher {
	return &Publisher{
		store:       s,
		bufferSize:  256,
		logger:      log.New(os.Stderr, "publisher: ", log.LstdFlags|log.Lmsgprefix),
		subscribers: make(map[string]*Subscriber),
		wakeup:      make(chan struct{}, 1),
	}
}

// SetBufferSize tunes the per-subscriber channel buffer. Tests use a
// small value (e.g. 2) to deliberately overflow.
func (p *Publisher) SetBufferSize(n int) {
	if n > 0 {
		p.bufferSize = n
	}
}

// Notify wakes the fan-out loop. Called by the tailer after every
// successful commit. Coalesced via a 1-buffer channel so a tight
// burst still results in one DB scan.
func (p *Publisher) Notify(_ string) {
	select {
	case p.wakeup <- struct{}{}:
	default:
	}
}

// Subscribe registers a new subscriber that wants events with seq >
// since. Returns the subscriber and a head-seq snapshot the caller can
// use to read the sessions table at a stable resource version.
//
// IMPORTANT: subscribers are added BEFORE the caller reads the
// sessions snapshot. That order — register, read snapshot at known
// head_seq, then forward in-stream events with seq > head_seq — closes
// the snapshot/stream gap (anti-pattern #4).
func (p *Publisher) Subscribe(id string, since int64) (*Subscriber, int64, error) {
	headSeq, err := p.store.Queries().GetHeadSeq(context.Background())
	if err != nil {
		return nil, 0, err
	}
	sub := &Subscriber{
		ID:     id,
		Since:  since,
		events: make(chan *gruv1.SessionEvent, p.bufferSize),
	}
	p.mu.Lock()
	p.subscribers[id] = sub
	p.mu.Unlock()
	// Kick the fan-out so the new subscriber gets any events the
	// caller has already missed (since < head_seq).
	p.Notify(id)
	return sub, headSeq, nil
}

// Unsubscribe removes a subscriber and closes its channel. Idempotent.
func (p *Publisher) Unsubscribe(id string) {
	p.mu.Lock()
	sub, ok := p.subscribers[id]
	if ok {
		delete(p.subscribers, id)
	}
	p.mu.Unlock()
	if ok && sub.closed.CompareAndSwap(false, true) {
		close(sub.events)
	}
}

// Run is the fan-out loop. It blocks until ctx is cancelled. Should
// be invoked in a single dedicated goroutine.
func (p *Publisher) Run(ctx context.Context) {
	// Periodic poll backstop in case wakeups are missed (shouldn't
	// happen, but the cost is one cheap SELECT per 250 ms).
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.shutdown()
			return
		case <-p.wakeup:
		case <-ticker.C:
		}
		p.flush(ctx)
	}
}

// flush queries new events for each subscriber and pushes them to the
// channel. On overflow, closes the subscriber's channel.
func (p *Publisher) flush(ctx context.Context) {
	// Snapshot the subscriber set under the lock; the actual sends
	// happen lock-free so a slow subscriber can't block other
	// subscribers' fan-out.
	p.mu.Lock()
	subs := make([]*Subscriber, 0, len(p.subscribers))
	for _, s := range p.subscribers {
		subs = append(subs, s)
	}
	p.mu.Unlock()

	// Each subscriber has its own `since`. For 5–20 sessions and 1
	// subscriber per UI client, the total fan-out is small enough
	// that one query per subscriber is fine. If the fleet grows we
	// can amortize via a single highest-seq scan; not now.
	for _, sub := range subs {
		if sub.IsClosed() {
			continue
		}
		const limit = 1000
		rows, err := p.store.Queries().ListEventsAfterSeq(ctx, store.ListEventsAfterSeqParams{
			Seq: sub.Since,
			Lim: limit,
		})
		if err != nil {
			p.logger.Printf("list events after seq=%d: %v", sub.Since, err)
			continue
		}
		for _, r := range rows {
			evt := rowToProto(r)
			if !p.deliver(sub, evt) {
				// Channel was overflowed; deliver closed it. Stop
				// trying to fan out to this subscriber for now.
				break
			}
			sub.Since = r.Seq
		}
	}
}

// deliver attempts a non-blocking send. On overflow it closes the
// subscriber's channel and returns false. The caller stops sending to
// this subscriber and the consumer's read of a closed channel surfaces
// the disconnect (anti-pattern #1: never silently drop).
func (p *Publisher) deliver(sub *Subscriber, evt *gruv1.SessionEvent) bool {
	select {
	case sub.events <- evt:
		return true
	default:
		// Buffer full → kick the slow subscriber.
		if sub.closed.CompareAndSwap(false, true) {
			close(sub.events)
		}
		p.mu.Lock()
		delete(p.subscribers, sub.ID)
		p.mu.Unlock()
		p.logger.Printf("subscriber %s overflowed; closing channel (last seq sent=%d)", sub.ID, sub.Since)
		return false
	}
}

func (p *Publisher) shutdown() {
	p.mu.Lock()
	subs := p.subscribers
	p.subscribers = map[string]*Subscriber{}
	p.mu.Unlock()
	for _, sub := range subs {
		if sub.closed.CompareAndSwap(false, true) {
			close(sub.events)
		}
	}
}

// Publish is an alias for PublishSynthetic, satisfying the artifacts.Publisher
// interface. Artifact events are synthetic — they don't live in the events table.
func (p *Publisher) Publish(evt *gruv1.SessionEvent) { p.PublishSynthetic(evt) }

// PublishSynthetic injects a one-off event that doesn't come from the
// events table — e.g. session.created at launch time, session.deleted
// when a row is removed. The event is fanned out to every active
// subscriber but NOT inserted into the events projection (so it
// doesn't get a `seq` and can't be replayed).
//
// Use sparingly. Most signals should flow through the events table.
func (p *Publisher) PublishSynthetic(evt *gruv1.SessionEvent) {
	p.mu.Lock()
	subs := make([]*Subscriber, 0, len(p.subscribers))
	for _, s := range p.subscribers {
		subs = append(subs, s)
	}
	p.mu.Unlock()
	for _, sub := range subs {
		if sub.IsClosed() {
			continue
		}
		p.deliver(sub, evt)
	}
}

func rowToProto(r store.Event) *gruv1.SessionEvent {
	t, _ := time.Parse(time.RFC3339, r.Timestamp)
	return &gruv1.SessionEvent{
		Id:        r.ID,
		Seq:       r.Seq,
		SessionId: r.SessionID,
		ProjectId: r.ProjectID,
		Runtime:   r.Runtime,
		Type:      r.Type,
		Timestamp: timestamppb.New(t),
		Payload:   []byte(r.Payload),
	}
}
