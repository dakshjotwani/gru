// Package devices is the registry of PWA installs that have subscribed
// to receive Web Push notifications. Each device is identified by an
// opaque UUID; its row stores the browser-provided push subscription
// (endpoint + VAPID-handshake keys) plus a per-device secret used to
// HMAC-sign approve/deny action tokens embedded in push payloads.
//
// The server is bound to the tailnet interface (see
// internal/server/bind.go), so device-registration endpoints are
// operator-only and no caller authentication is required.
package devices

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/dakshjotwani/gru/internal/store/db"
	"github.com/google/uuid"
)

// Registry wraps the sqlc-generated queries with the small amount of
// domain logic that doesn't belong in SQL (generating IDs, minting
// per-device secrets).
type Registry struct {
	q db.Querier
}

// NewRegistry returns a Registry backed by a sqlc Querier.
func NewRegistry(q db.Querier) *Registry {
	return &Registry{q: q}
}

// Subscription is the browser-provided Web Push subscription.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Device is a registered PWA install.
type Device struct {
	ID                string
	Label             string
	Subscription      Subscription
	ActionTokenSecret []byte // raw 32 bytes
	CreatedAt         string
	LastSeenAt        string
}

// Register creates a new device row. The caller provides a label and
// subscription; the Registry mints the UUID and action-token secret.
func (r *Registry) Register(ctx context.Context, label string, sub Subscription) (*Device, error) {
	if label == "" {
		return nil, fmt.Errorf("devices: label is required")
	}
	if sub.Endpoint == "" || sub.P256dh == "" || sub.Auth == "" {
		return nil, fmt.Errorf("devices: subscription endpoint + p256dh + auth are all required")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("devices: generate action-token secret: %w", err)
	}
	row, err := r.q.CreateDevice(ctx, db.CreateDeviceParams{
		ID:                uuid.NewString(),
		Label:             label,
		PushEndpoint:      sub.Endpoint,
		PushP256dh:        sub.P256dh,
		PushAuth:          sub.Auth,
		ActionTokenSecret: hex.EncodeToString(secret),
	})
	if err != nil {
		return nil, fmt.Errorf("devices: create: %w", err)
	}
	return rowToDevice(row)
}

// Get returns a device by ID, or an error if not found.
func (r *Registry) Get(ctx context.Context, id string) (*Device, error) {
	row, err := r.q.GetDevice(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("devices: get: %w", err)
	}
	return rowToDevice(row)
}

// List returns non-stale devices, ordered oldest-first.
func (r *Registry) List(ctx context.Context) ([]*Device, error) {
	rows, err := r.q.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("devices: list: %w", err)
	}
	out := make([]*Device, 0, len(rows))
	for _, row := range rows {
		d, err := rowToDevice(row)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// UpdateSubscription rotates a device's push subscription. Called by
// the PWA service worker on `pushsubscriptionchange`.
func (r *Registry) UpdateSubscription(ctx context.Context, id string, sub Subscription) error {
	if sub.Endpoint == "" || sub.P256dh == "" || sub.Auth == "" {
		return fmt.Errorf("devices: subscription endpoint + p256dh + auth are all required")
	}
	_, err := r.q.UpdateDeviceSubscription(ctx, db.UpdateDeviceSubscriptionParams{
		ID:           id,
		PushEndpoint: sub.Endpoint,
		PushP256dh:   sub.P256dh,
		PushAuth:     sub.Auth,
	})
	if err != nil {
		return fmt.Errorf("devices: update: %w", err)
	}
	return nil
}

// MarkStale flags a device whose push endpoint is permanently failing,
// so the push dispatcher stops trying. The PWA check-in path clears
// the flag automatically via UpdateSubscription.
func (r *Registry) MarkStale(ctx context.Context, id string) error {
	return r.q.MarkDeviceStale(ctx, id)
}

// Delete removes a device (operator revocation).
func (r *Registry) Delete(ctx context.Context, id string) error {
	return r.q.DeleteDevice(ctx, id)
}

// RecordAction writes an idempotency row. Returns an error if the
// (eventID, action) pair has already been recorded — the caller should
// treat that as "already resolved" and respond 409.
func (r *Registry) RecordAction(ctx context.Context, eventID, action, deviceID string) error {
	return r.q.RecordAction(ctx, db.RecordActionParams{
		EventID:  eventID,
		Action:   action,
		DeviceID: deviceID,
	})
}

func rowToDevice(row db.Device) (*Device, error) {
	secret, err := hex.DecodeString(row.ActionTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("devices: decode action_token_secret: %w", err)
	}
	return &Device{
		ID:                row.ID,
		Label:             row.Label,
		Subscription:      Subscription{Endpoint: row.PushEndpoint, P256dh: row.PushP256dh, Auth: row.PushAuth},
		ActionTokenSecret: secret,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
	}, nil
}
