package push_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/devices"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/push"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mustP256 / mustAuth produce valid-curve subscription keys for the
// fake endpoint. webpush-go's sender validates the point on the
// curve before encrypting, so the key has to be real even though
// the fake endpoint never decrypts the payload.
func mustP256(t *testing.T) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
}

func mustAuth(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// fakePushEndpoint captures the requests the dispatcher makes. One
// instance per test device; the endpoint URL goes into the
// Subscription we register.
type fakePushEndpoint struct {
	mu      sync.Mutex
	calls   []fakeCall
	status  int // status to return (defaults to 201)
	server  *httptest.Server
}

type fakeCall struct {
	body []byte
}

func newFakePushEndpoint(t *testing.T) *fakePushEndpoint {
	t.Helper()
	f := &fakePushEndpoint{status: http.StatusCreated}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{body: body})
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePushEndpoint) endpoint() string { return f.server.URL }

func (f *fakePushEndpoint) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestDispatcher_NeedsAttentionPushesWithActions exercises the full
// pipeline: publish a needs_attention event → assert the dispatcher
// HTTP-hit the fake endpoint → decode the encrypted body back to
// verify action buttons are set.
// (We don't decrypt the ECDH payload in-test — we just assert a hit
// was made. Upstream webpush-go tests cover the cryptography.)
func TestDispatcher_NeedsAttentionPushesOnce(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	// Seed project + session so the dispatcher can fetch labels.
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "Proj", Adapter: "host", Runtime: "claude-code",
	})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "claude-code", Status: "running",
	})

	reg := devices.NewRegistry(s.Queries())
	fake := newFakePushEndpoint(t)
	_, err = reg.Register(ctx, "phone", devices.Subscription{
		Endpoint: fake.endpoint(), P256dh: mustP256(t), Auth: mustAuth(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	priv, pub, _ := push.GenerateVAPIDKeys()

	pubPub := ingestion.NewPublisher()
	disp := push.NewDispatcher(push.Config{
		VAPIDPrivateKey: priv,
		VAPIDPublicKey:  pub,
		Subject:         "mailto:test@gru.local",
		Threshold:       0.7,
		RateLimit:       50 * time.Millisecond,
	}, reg, pubPub, s)

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go disp.Run(dctx)
	// small wait to let Run() subscribe before we publish
	time.Sleep(20 * time.Millisecond)

	evt := &gruv1.SessionEvent{
		Id:        "evt-1",
		SessionId: "sess-1",
		ProjectId: "proj-1",
		Runtime:   "claude-code",
		Type:      "notification.needs_attention",
		Timestamp: timestamppb.Now(),
		Payload:   []byte(`{"message":"need your approval"}`),
	}
	pubPub.Publish(evt)

	// Wait up to 2s for the fake endpoint to get hit.
	if !eventuallyTrue(2*time.Second, func() bool { return fake.callCount() >= 1 }) {
		t.Fatalf("expected 1 push, got %d", fake.callCount())
	}

	// Second publish within rate-limit window must NOT send.
	evt2 := *evt
	evt2.Id = "evt-2"
	pubPub.Publish(&evt2)
	time.Sleep(30 * time.Millisecond)
	if fake.callCount() != 1 {
		t.Errorf("rate limit not honored: got %d calls, want 1", fake.callCount())
	}
}

func TestDispatcher_IdleBelowThresholdIgnored(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{ID: "p", Name: "P", Adapter: "host", Runtime: "claude-code"})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{ID: "sess-1", ProjectID: "p", Runtime: "claude-code", Status: "idle"})
	_, _ = s.Queries().UpdateSessionAttentionScore(ctx, store.UpdateSessionAttentionScoreParams{ID: "sess-1", AttentionScore: 0.3})

	reg := devices.NewRegistry(s.Queries())
	fake := newFakePushEndpoint(t)
	_, _ = reg.Register(ctx, "phone", devices.Subscription{
		Endpoint: fake.endpoint(),
		P256dh:   mustP256(t),
		Auth:     mustAuth(t),
	})

	priv, pub, _ := push.GenerateVAPIDKeys()
	pubPub := ingestion.NewPublisher()
	disp := push.NewDispatcher(push.Config{
		VAPIDPrivateKey: priv, VAPIDPublicKey: pub, Subject: "mailto:t@x", Threshold: 0.7, RateLimit: 10 * time.Millisecond,
	}, reg, pubPub, s)

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go disp.Run(dctx)
	time.Sleep(20 * time.Millisecond)

	pubPub.Publish(&gruv1.SessionEvent{
		Id: "evt-x", SessionId: "sess-1", ProjectId: "p", Runtime: "claude-code",
		Type: "session.idle", Timestamp: timestamppb.Now(),
	})
	time.Sleep(100 * time.Millisecond)
	if fake.callCount() != 0 {
		t.Errorf("expected no push (score below threshold), got %d", fake.callCount())
	}
}

func TestDispatcher_StaleEndpointMarked(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{ID: "p", Name: "P", Adapter: "host", Runtime: "claude-code"})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{ID: "sess-1", ProjectID: "p", Runtime: "claude-code", Status: "running"})

	reg := devices.NewRegistry(s.Queries())
	fake := newFakePushEndpoint(t)
	fake.status = http.StatusGone // endpoint permanently dead

	dev, _ := reg.Register(ctx, "phone", devices.Subscription{
		Endpoint: fake.endpoint(),
		P256dh:   mustP256(t),
		Auth:     mustAuth(t),
	})

	priv, pub, _ := push.GenerateVAPIDKeys()
	pubPub := ingestion.NewPublisher()
	disp := push.NewDispatcher(push.Config{
		VAPIDPrivateKey: priv, VAPIDPublicKey: pub, Subject: "mailto:t@x", Threshold: 0.7, RateLimit: 10 * time.Millisecond,
	}, reg, pubPub, s)

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go disp.Run(dctx)
	time.Sleep(20 * time.Millisecond)

	pubPub.Publish(&gruv1.SessionEvent{
		Id: "evt-1", SessionId: "sess-1", ProjectId: "p", Runtime: "claude-code",
		Type: "notification.needs_attention", Timestamp: timestamppb.Now(),
		Payload: []byte(`{"message":"x"}`),
	})

	if !eventuallyTrue(2*time.Second, func() bool {
		list, _ := reg.List(ctx)
		// List returns only non-stale; once marked stale, list shrinks to 0.
		return len(list) == 0
	}) {
		t.Fatalf("expected device to be marked stale after 410")
	}
	_ = dev
}

// eventuallyTrue polls fn every 20ms until it returns true or the
// deadline passes. Returns the final value of fn().
func eventuallyTrue(d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

