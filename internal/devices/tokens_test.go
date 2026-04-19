package devices_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/devices"
)

func TestMintAndVerify_roundtrip(t *testing.T) {
	secret := []byte("super-secret-32-bytes-aaaaaaaaaa")
	exp := time.Now().Add(5 * time.Minute)
	tok := devices.MintActionToken(secret, "dev-1", "evt-1", exp)

	at, err := devices.VerifyActionToken(tok, secret, time.Now())
	if err != nil {
		t.Fatalf("VerifyActionToken: %v", err)
	}
	if at.DeviceID != "dev-1" {
		t.Errorf("DeviceID = %q, want dev-1", at.DeviceID)
	}
	if at.EventID != "evt-1" {
		t.Errorf("EventID = %q, want evt-1", at.EventID)
	}
	if !at.Expires.Equal(exp.Truncate(time.Second)) {
		t.Errorf("Expires = %v, want %v", at.Expires, exp.Truncate(time.Second))
	}
}

func TestVerify_expired(t *testing.T) {
	secret := []byte("super-secret-32-bytes-aaaaaaaaaa")
	exp := time.Now().Add(-1 * time.Minute) // already past
	tok := devices.MintActionToken(secret, "dev-1", "evt-1", exp)

	_, err := devices.VerifyActionToken(tok, secret, time.Now())
	if !errors.Is(err, devices.ErrActionTokenExpired) {
		t.Fatalf("expected ErrActionTokenExpired, got %v", err)
	}
}

func TestVerify_wrongSecret(t *testing.T) {
	minted := devices.MintActionToken([]byte("aaaa"), "dev-1", "evt-1", time.Now().Add(5*time.Minute))
	_, err := devices.VerifyActionToken(minted, []byte("bbbb"), time.Now())
	if !errors.Is(err, devices.ErrActionTokenBadSignature) {
		t.Fatalf("expected ErrActionTokenBadSignature, got %v", err)
	}
}

func TestVerify_malformed(t *testing.T) {
	cases := []string{
		"",
		"just-one",
		"one.two",
		"one.two.three",
		"one.two.three.not-a-number",
	}
	for _, tc := range cases {
		_, err := devices.VerifyActionToken(tc, []byte("secret"), time.Now())
		if !errors.Is(err, devices.ErrActionTokenMalformed) {
			t.Errorf("%q: expected ErrActionTokenMalformed, got %v", tc, err)
		}
	}
}

func TestVerify_crossDeviceReplayBlocked(t *testing.T) {
	// A token minted by device A's secret MUST fail when verified against
	// device B's secret, even if DeviceID in the payload matches A.
	secretA := []byte("aaaa-device-a-secret-0000000000000")
	secretB := []byte("bbbb-device-b-secret-0000000000000")
	tok := devices.MintActionToken(secretA, "dev-A", "evt-1", time.Now().Add(5*time.Minute))

	_, err := devices.VerifyActionToken(tok, secretB, time.Now())
	if !errors.Is(err, devices.ErrActionTokenBadSignature) {
		t.Fatalf("expected ErrActionTokenBadSignature, got %v", err)
	}
}
