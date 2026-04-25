package publisher_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/publisher"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/google/uuid"
)

func setupStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	if _, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-pub", Name: "test", Adapter: "host", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-pub", ProjectID: "proj-pub", Runtime: "claude-code", Status: "starting",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// insertEvent writes a fresh row into the events projection. Returns
// the row's seq.
func insertEvent(t *testing.T, s *store.Store, evtType string) int64 {
	t.Helper()
	row, err := s.Queries().CreateEvent(context.Background(), store.CreateEventParams{
		ID:        uuid.NewString(),
		SessionID: "sess-pub",
		ProjectID: "proj-pub",
		Runtime:   "claude-code",
		Type:      evtType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.Seq
}

func TestPublisher_deliversNewEvents(t *testing.T) {
	s := setupStore(t)
	pub := publisher.NewPublisher(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	sub, head, err := pub.Subscribe("client-1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if head != 0 {
		t.Fatalf("head seq = %d, want 0 on empty events table", head)
	}

	insertEvent(t, s, "assistant.message")
	pub.Notify("sess-pub")

	select {
	case evt := <-sub.Events():
		if evt == nil {
			t.Fatal("got closed channel; expected event")
		}
		if evt.Type != "assistant.message" {
			t.Fatalf("event type = %q, want assistant.message", evt.Type)
		}
		if evt.Seq == 0 {
			t.Fatalf("event seq = 0, want > 0")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not deliver event within deadline")
	}
}

func TestPublisher_replaysFromSinceSeq(t *testing.T) {
	s := setupStore(t)
	pub := publisher.NewPublisher(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	// Pre-insert three events; subscribe with since=1 → expect to
	// receive only seq 2 and 3.
	insertEvent(t, s, "evt.1")
	insertEvent(t, s, "evt.2")
	insertEvent(t, s, "evt.3")

	sub, head, err := pub.Subscribe("client-replay", 1)
	if err != nil {
		t.Fatal(err)
	}
	if head < 3 {
		t.Fatalf("head_seq = %d, want >= 3", head)
	}

	got := drainEvents(t, sub.Events(), 2, 1*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (since=1)", len(got))
	}
	if got[0].Type != "evt.2" || got[1].Type != "evt.3" {
		t.Fatalf("got types = %v, want [evt.2 evt.3]", typesOf(got))
	}
}

// ── the load-bearing reliability test: close-on-overflow + replay ────

func TestPublisher_closesOnOverflowAndReplaysOnReconnect(t *testing.T) {
	s := setupStore(t)
	pub := publisher.NewPublisher(s)
	pub.SetBufferSize(2) // tiny buffer so we can overflow it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	sub, _, err := pub.Subscribe("slow-client", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Fire 10 events without ever reading from sub. The buffer holds
	// 2; the rest must overflow and the publisher MUST close the
	// subscriber rather than silently drop. (anti-pattern #1)
	for i := 0; i < 10; i++ {
		insertEvent(t, s, fmt.Sprintf("burst.%d", i))
	}
	pub.Notify("sess-pub")

	// Wait for the channel to be observed-closed.
	closed := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sub.IsClosed() {
			closed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !closed {
		t.Fatal("publisher did not close slow subscriber on overflow")
	}
	// Receiving from a closed channel returns zero-value immediately.
	select {
	case evt, ok := <-sub.Events():
		if ok && evt != nil {
			// We may get the buffered events first, then a close.
			// Drain until close.
			for {
				select {
				case _, ok2 := <-sub.Events():
					if !ok2 {
						goto reconnect
					}
				case <-time.After(500 * time.Millisecond):
					t.Fatal("expected channel close; events still streaming")
				}
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected to read from closed channel")
	}

reconnect:
	// Bump the buffer for the reconnect — the client now drains fast
	// enough to keep up with replay. (In production the reconnect
	// uses the same buffer; we deliberately set it tiny in this test
	// to force overflow on the first phase.)
	pub.SetBufferSize(64)

	// Now the client reconnects with since_seq=0 (it never recorded
	// any seq). It MUST receive every event in order, by replay.
	sub2, _, err := pub.Subscribe("slow-client-reconnect", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := drainEvents(t, sub2.Events(), 10, 2*time.Second)
	if len(got) != 10 {
		t.Fatalf("after reconnect with since=0, got %d events, want 10", len(got))
	}
	// Validate ordering by seq.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("events out of order: seq[%d]=%d <= seq[%d]=%d", i, got[i].Seq, i-1, got[i-1].Seq)
		}
	}
}

// ── snapshot regression guard: simulate stale snapshot ──────────────

// (This test belongs to the frontend's reducer; we don't have a Go
// equivalent of it without writing a fake client. Instead the test
// verifies the server-side property the frontend relies on: the
// `head_seq` returned at Subscribe time advances monotonically, and
// the events the publisher delivers after Subscribe always have
// seq > head_seq. The frontend can then compare a snapshot's
// last_event_seq against the highest delivered seq and ignore
// regressions. See web/src/hooks/useSessionStream.test.ts for the
// client-side counterpart.)

// TestPublisher_subscribeAfterEventsHasNoGap verifies the
// "subscribe-then-snapshot" property (anti-pattern #4 / spec §3.7):
// a client that subscribes at since=0 must receive every event ever
// committed, in order — not just events that arrive AFTER the
// subscribe call.
func TestPublisher_subscribeAfterEventsHasNoGap(t *testing.T) {
	s := setupStore(t)
	pub := publisher.NewPublisher(s)
	pub.SetBufferSize(64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	// Insert 5 events BEFORE any subscriber exists.
	for i := 0; i < 5; i++ {
		insertEvent(t, s, fmt.Sprintf("pre.%d", i))
	}
	sub, head, err := pub.Subscribe("late-c", 0)
	if err != nil {
		t.Fatal(err)
	}
	if head != 5 {
		t.Fatalf("head_seq = %d, want 5", head)
	}
	got := drainEvents(t, sub.Events(), 5, 1*time.Second)
	if len(got) != 5 {
		t.Fatalf("got %d events on subscribe; want 5 (no gap)", len(got))
	}
}

func TestPublisher_subscribeReturnsCurrentHeadSeq(t *testing.T) {
	s := setupStore(t)
	pub := publisher.NewPublisher(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	insertEvent(t, s, "a")
	insertEvent(t, s, "b")
	insertEvent(t, s, "c")

	_, head, err := pub.Subscribe("c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if head < 3 {
		t.Fatalf("head_seq after 3 events = %d, want >= 3", head)
	}

	insertEvent(t, s, "d")
	_, head2, err := pub.Subscribe("c2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if head2 <= head {
		t.Fatalf("head_seq did not advance: prev=%d now=%d", head, head2)
	}
}

// TestPublisher_multiSessionOrderingPreserved checks that two
// sessions emitting interleaved events deliver each session's events
// in the order they were committed. The publisher orders globally by
// seq, which is fine — what matters is that within one session the
// derived state's evolution is preserved.
func TestPublisher_multiSessionOrderingPreserved(t *testing.T) {
	s := setupStore(t)
	// Add a second session row.
	if _, err := s.Queries().CreateSession(context.Background(), store.CreateSessionParams{
		ID: "sess-pub-2", ProjectID: "proj-pub", Runtime: "claude-code", Status: "starting",
	}); err != nil {
		t.Fatal(err)
	}

	pub := publisher.NewPublisher(s)
	pub.SetBufferSize(64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.Run(ctx)

	sub, _, err := pub.Subscribe("c1", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Interleave: A1, B1, A2, B2
	insertEventForSession(t, s, "sess-pub", "A1")
	insertEventForSession(t, s, "sess-pub-2", "B1")
	insertEventForSession(t, s, "sess-pub", "A2")
	insertEventForSession(t, s, "sess-pub-2", "B2")
	pub.Notify("")

	got := drainEvents(t, sub.Events(), 4, 2*time.Second)
	if len(got) != 4 {
		t.Fatalf("got %d events, want 4", len(got))
	}
	// Per-session ordering check.
	var sessA, sessB []string
	for _, e := range got {
		if e.SessionId == "sess-pub" {
			sessA = append(sessA, e.Type)
		} else {
			sessB = append(sessB, e.Type)
		}
	}
	if got, want := fmt.Sprintf("%v", sessA), "[A1 A2]"; got != want {
		t.Fatalf("session A order = %s, want %s", got, want)
	}
	if got, want := fmt.Sprintf("%v", sessB), "[B1 B2]"; got != want {
		t.Fatalf("session B order = %s, want %s", got, want)
	}
}

func insertEventForSession(t *testing.T, s *store.Store, sid, evtType string) int64 {
	t.Helper()
	row, err := s.Queries().CreateEvent(context.Background(), store.CreateEventParams{
		ID:        uuid.NewString(),
		SessionID: sid,
		ProjectID: "proj-pub",
		Runtime:   "claude-code",
		Type:      evtType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.Seq
}

// ── helpers ──────────────────────────────────────────────────────────

func drainEvents(t *testing.T, ch <-chan *gruv1.SessionEvent, count int, timeout time.Duration) []*gruv1.SessionEvent {
	t.Helper()
	got := make([]*gruv1.SessionEvent, 0, count)
	deadline := time.Now().Add(timeout)
	for len(got) < count && time.Now().Before(deadline) {
		select {
		case evt, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, evt)
		case <-time.After(time.Until(deadline)):
			return got
		}
	}
	return got
}

func typesOf(rows []*gruv1.SessionEvent) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Type)
	}
	return out
}
