package devices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/devices"
	"github.com/dakshjotwani/gru/internal/store"
)

// newTestServer spins up a devices handler backed by an in-memory
// SQLite store. Returns the http test server, the registry (for
// setup/inspection), and the capture-struct the test uses to assert
// what the action resolver was called with.
func newTestServer(t *testing.T, now time.Time) (*httptest.Server, *devices.Registry, *resolverCapture) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	reg := devices.NewRegistry(s.Queries())
	cap := &resolverCapture{sessionIDForEvent: map[string]string{}}

	mux := devices.Mux(devices.HandlerDeps{
		Registry: reg,
		Resolve:  cap.resolve,
		Lookup:   cap.lookup,
		Now:      func() time.Time { return now },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, reg, cap
}

type resolverCapture struct {
	sessionIDForEvent map[string]string
	resolvedSession   string
	resolvedInput     string
}

func (c *resolverCapture) resolve(_ context.Context, sessionID, input string) error {
	c.resolvedSession = sessionID
	c.resolvedInput = input
	return nil
}

func (c *resolverCapture) lookup(_ context.Context, eventID string) (string, bool) {
	s, ok := c.sessionIDForEvent[eventID]
	return s, ok
}

func TestHTTP_RegisterAndListDevice(t *testing.T) {
	ts, _, _ := newTestServer(t, time.Now())

	body := `{"label":"test-phone","endpoint":"https://push.example/abc","p256dh":"pk","auth":"au"}`
	resp, err := http.Post(ts.URL+"/devices", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d", resp.StatusCode)
	}
	var reg struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	if reg.ID == "" {
		t.Fatal("expected non-empty device ID")
	}

	// GET /devices returns exactly one entry.
	getResp, err := http.Get(ts.URL + "/devices")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(getResp.Body).Decode(&list)
	getResp.Body.Close()
	if len(list) != 1 || list[0]["label"] != "test-phone" {
		t.Fatalf("list = %#v", list)
	}
}

func TestHTTP_ActionApprove_EndToEnd(t *testing.T) {
	now := time.Now()
	ts, reg, cap := newTestServer(t, now)

	// Register a device.
	dev, err := reg.Register(context.Background(), "my-phone", devices.Subscription{
		Endpoint: "https://e", P256dh: "p", Auth: "a",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pretend there's a pending permission prompt for event X on session Y.
	cap.sessionIDForEvent["evt-1"] = "sess-Y"

	token := devices.MintActionToken(dev.ActionTokenSecret, dev.ID, "evt-1", now.Add(5*time.Minute))

	resp, err := http.Post(ts.URL+"/actions/"+token+"?a=approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(resp.Body)
		t.Fatalf("approve status = %d body=%s", resp.StatusCode, b.String())
	}

	if cap.resolvedSession != "sess-Y" {
		t.Errorf("resolvedSession = %q, want sess-Y", cap.resolvedSession)
	}
	if cap.resolvedInput != "1\n" {
		t.Errorf("resolvedInput = %q, want 1\\n", cap.resolvedInput)
	}

	// Double-tap: second attempt must 409 with idempotency, and MUST NOT
	// re-invoke the resolver (reset the capture and verify).
	cap.resolvedSession = ""
	cap.resolvedInput = ""
	resp2, err := http.Post(ts.URL+"/actions/"+token+"?a=approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("double-tap status = %d, want 409", resp2.StatusCode)
	}
	if cap.resolvedSession != "" || cap.resolvedInput != "" {
		t.Errorf("resolver should not have been called on double-tap, got session=%q input=%q",
			cap.resolvedSession, cap.resolvedInput)
	}
}

func TestHTTP_ActionExpired(t *testing.T) {
	// Now is after the token's expiry.
	serverNow := time.Now()
	ts, reg, cap := newTestServer(t, serverNow)

	dev, _ := reg.Register(context.Background(), "p", devices.Subscription{Endpoint: "e", P256dh: "p", Auth: "a"})
	cap.sessionIDForEvent["evt-1"] = "sess-Y"

	// Mint with an expiry 1 minute before serverNow.
	token := devices.MintActionToken(dev.ActionTokenSecret, dev.ID, "evt-1", serverNow.Add(-1*time.Minute))

	resp, err := http.Post(ts.URL+"/actions/"+token+"?a=approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 Gone", resp.StatusCode)
	}
}

func TestHTTP_ActionInvalidSignature(t *testing.T) {
	ts, reg, cap := newTestServer(t, time.Now())
	dev, _ := reg.Register(context.Background(), "p", devices.Subscription{Endpoint: "e", P256dh: "p", Auth: "a"})
	cap.sessionIDForEvent["evt-1"] = "sess-Y"

	// Tamper with the signature portion.
	tok := devices.MintActionToken(dev.ActionTokenSecret, dev.ID, "evt-1", time.Now().Add(5*time.Minute))
	parts := strings.SplitN(tok, ".", 4)
	parts[0] = "AAAA" // garbage sig
	tampered := strings.Join(parts, ".")

	resp, err := http.Post(ts.URL+"/actions/"+tampered+"?a=approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
