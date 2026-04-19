// Package push is the Web Push dispatcher for Gru.
//
// Pipeline:
//
//	ingestion.Publisher → Dispatcher → VAPID-signed push → APNs / FCM
//
// The Dispatcher subscribes to the same Publisher that feeds
// SubscribeEvents, so every GruEvent flowing through the server is
// visible here. Trigger policy (see docs/superpowers/specs/
// 2026-04-18-gru-off-terminal-design.md):
//
//   - notification.needs_attention → always push, with Approve/Deny
//     action buttons (signed per-device action token in `data`).
//   - session.idle + attention_score > threshold → push, body is the
//     event summary.
//   - All other event types are ignored.
//
// Per-session rate limit: at most one push per session per
// RateLimit. Dedupe in the OS tray is handled client-side via the
// `tag: session_id` notification option.
package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/dakshjotwani/gru/internal/devices"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
)

// Config tunes the dispatcher. Zero values fall back to documented
// defaults.
type Config struct {
	// VAPIDPrivateKey / VAPIDPublicKey are urlbase64-raw encoded per RFC8292.
	VAPIDPrivateKey string
	VAPIDPublicKey  string
	// Subject is the VAPID mailto: or https: subject URL (e.g. "mailto:ops@gru.local").
	Subject string
	// Threshold gates session.idle pushes: attention_score must exceed it.
	Threshold float64
	// RateLimit is the minimum gap between pushes for the same session.
	RateLimit time.Duration
	// ActionTokenTTL is how long approve/deny tokens are valid. Keep short
	// — the lock-screen is actionable only while the user's nearby.
	ActionTokenTTL time.Duration
}

func (c *Config) withDefaults() {
	if c.Threshold == 0 {
		c.Threshold = 0.7
	}
	if c.RateLimit == 0 {
		c.RateLimit = 30 * time.Second
	}
	if c.ActionTokenTTL == 0 {
		c.ActionTokenTTL = 5 * time.Minute
	}
	if c.Subject == "" {
		c.Subject = "mailto:operator@gru.local"
	}
}

// Dispatcher fans out GruEvents to registered devices as Web Push.
type Dispatcher struct {
	cfg    Config
	reg    *devices.Registry
	pub    *ingestion.Publisher
	store  *store.Store
	client *http.Client

	mu       sync.Mutex
	lastPush map[string]time.Time // sessionID → last push time, for rate limiting
}

// NewDispatcher wires the dispatcher; call Run to start its goroutine.
func NewDispatcher(cfg Config, reg *devices.Registry, pub *ingestion.Publisher, s *store.Store) *Dispatcher {
	cfg.withDefaults()
	return &Dispatcher{
		cfg:      cfg,
		reg:      reg,
		pub:      pub,
		store:    s,
		client:   &http.Client{Timeout: 10 * time.Second},
		lastPush: make(map[string]time.Time),
	}
}

// Run subscribes to the publisher and dispatches pushes for matching
// events until ctx is cancelled. Safe to call in a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	ch := make(chan *gruv1.SessionEvent, 64)
	subID := "push-dispatcher"
	d.pub.Subscribe(subID, ch)
	defer d.pub.Unsubscribe(subID)

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			if evt == nil {
				continue
			}
			if err := d.handle(ctx, evt); err != nil {
				log.Printf("push: handle %s: %v", evt.Id, err)
			}
		}
	}
}

// handle applies the trigger policy for a single event and dispatches
// to all non-stale devices if the event qualifies.
func (d *Dispatcher) handle(ctx context.Context, evt *gruv1.SessionEvent) error {
	trigger, body := d.classify(ctx, evt)
	if trigger == triggerNone {
		return nil
	}
	if !d.rateLimitAllow(evt.SessionId) {
		return nil
	}

	// Human title: "<project> — <session>".
	projectName, sessionName := d.sessionLabels(ctx, evt.SessionId)
	title := projectName
	if sessionName != "" {
		title = projectName + " — " + sessionName
	}

	rows, err := d.reg.List(ctx)
	if err != nil {
		return fmt.Errorf("list devices: %w", err)
	}
	for _, dev := range rows {
		payload := d.buildPayload(evt, title, body, trigger, dev)
		if err := d.send(ctx, dev, payload); err != nil {
			log.Printf("push: device=%s: %v", dev.ID, err)
		}
	}
	return nil
}

type trigger int

const (
	triggerNone      trigger = 0
	triggerAttention trigger = 1 // needs_attention, actionable
	triggerIdle      trigger = 2 // plain idle over threshold, navigation-only
)

// classify maps an event to a trigger kind + body text. Returns
// triggerNone for events that shouldn't push.
func (d *Dispatcher) classify(ctx context.Context, evt *gruv1.SessionEvent) (trigger, string) {
	switch evt.Type {
	case "notification.needs_attention":
		return triggerAttention, shorten(extractMessage(evt.Payload), 80)
	case "session.idle":
		sess, err := d.store.Queries().GetSession(ctx, evt.SessionId)
		if err != nil || sess.AttentionScore <= d.cfg.Threshold {
			return triggerNone, ""
		}
		return triggerIdle, "turn complete — needs your input"
	}
	return triggerNone, ""
}

func (d *Dispatcher) rateLimitAllow(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.lastPush[sessionID]; ok {
		if time.Since(last) < d.cfg.RateLimit {
			return false
		}
	}
	d.lastPush[sessionID] = time.Now()
	return true
}

// Payload is the wire format sent to the service worker's `push`
// handler. Fields map 1:1 to the notification options.
type Payload struct {
	Title   string            `json:"title"`
	Body    string            `json:"body"`
	Tag     string            `json:"tag"`
	Actions []PayloadAction   `json:"actions,omitempty"`
	Data    map[string]string `json:"data"`
}

type PayloadAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
}

func (d *Dispatcher) buildPayload(evt *gruv1.SessionEvent, title, body string, t trigger, dev *devices.Device) Payload {
	p := Payload{
		Title: title,
		Body:  body,
		Tag:   evt.SessionId,
		Data:  map[string]string{"sessionId": evt.SessionId, "eventId": evt.Id},
	}
	if t == triggerAttention {
		tok := devices.MintActionToken(
			dev.ActionTokenSecret,
			dev.ID,
			evt.Id,
			time.Now().Add(d.cfg.ActionTokenTTL),
		)
		p.Data["actionToken"] = tok
		p.Actions = []PayloadAction{
			{Action: "approve", Title: "Approve"},
			{Action: "deny", Title: "Deny"},
		}
	}
	return p
}

// send delivers a single push to one device. Stale endpoints (404,
// 410) cause the device to be marked stale so the dispatcher stops
// retrying.
func (d *Dispatcher) send(ctx context.Context, dev *devices.Device, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	sub := &webpush.Subscription{
		Endpoint: dev.Subscription.Endpoint,
		Keys: webpush.Keys{
			P256dh: dev.Subscription.P256dh,
			Auth:   dev.Subscription.Auth,
		},
	}
	resp, err := webpush.SendNotificationWithContext(ctx, body, sub, &webpush.Options{
		Subscriber:      d.cfg.Subject,
		VAPIDPublicKey:  d.cfg.VAPIDPublicKey,
		VAPIDPrivateKey: d.cfg.VAPIDPrivateKey,
		TTL:             int(d.cfg.ActionTokenTTL.Seconds()),
		HTTPClient:      d.client,
	})
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		// Endpoint is permanently dead; tombstone so we stop trying.
		if err := d.reg.MarkStale(ctx, dev.ID); err != nil {
			log.Printf("push: mark stale device=%s: %v", dev.ID, err)
		}
		return fmt.Errorf("endpoint stale (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("push endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// sessionLabels fetches the project + session display names. Best-
// effort — empty strings fall back to "" which the title code handles.
func (d *Dispatcher) sessionLabels(ctx context.Context, sessionID string) (projectName, sessionName string) {
	row, err := d.store.Queries().GetSession(ctx, sessionID)
	if err != nil {
		return "Gru", ""
	}
	sessionName = row.Name
	proj, err := d.store.Queries().GetProject(ctx, row.ProjectID)
	if err == nil {
		projectName = proj.Name
	} else {
		projectName = "Gru"
	}
	return projectName, sessionName
}

func extractMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var pl struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &pl); err != nil {
		return ""
	}
	return pl.Message
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return s[:n-1] + "…"
}

// GenerateVAPIDKeys wraps webpush.GenerateVAPIDKeys so callers don't
// need to import the transitive package for a trivial helper.
func GenerateVAPIDKeys() (privateKey, publicKey string, err error) {
	return webpush.GenerateVAPIDKeys()
}
