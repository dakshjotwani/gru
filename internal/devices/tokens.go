package devices

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ActionToken is the payload embedded in a push notification's action
// data. Format on the wire:
//
//	<sig>.<deviceID>.<eventID>.<expiresUnix>
//
// where <sig> is a URL-safe-base64 HMAC-SHA256 over
// "<deviceID>|<eventID>|<expiresUnix>" keyed by the device's
// per-row ActionTokenSecret. Each device has its own secret so a token
// captured from one device can't be replayed against another.
type ActionToken struct {
	DeviceID string
	EventID  string
	Expires  time.Time
}

// ErrActionTokenMalformed is returned when the token doesn't parse.
var ErrActionTokenMalformed = errors.New("devices: action token malformed")

// ErrActionTokenExpired is returned when the token parses but is past Expires.
var ErrActionTokenExpired = errors.New("devices: action token expired")

// ErrActionTokenBadSignature is returned when the signature doesn't match.
var ErrActionTokenBadSignature = errors.New("devices: action token signature mismatch")

// MintActionToken returns the wire-format token string.
func MintActionToken(secret []byte, deviceID, eventID string, expires time.Time) string {
	expStr := strconv.FormatInt(expires.Unix(), 10)
	payload := deviceID + "|" + eventID + "|" + expStr
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sig + "." + deviceID + "." + eventID + "." + expStr
}

// ParseActionToken splits the wire format into its four parts. It does
// not verify the signature — call VerifyActionToken for that.
func ParseActionToken(token string) (sig, deviceID, eventID string, expires time.Time, err error) {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 4 {
		return "", "", "", time.Time{}, ErrActionTokenMalformed
	}
	sig, deviceID, eventID = parts[0], parts[1], parts[2]
	expUnix, parseErr := strconv.ParseInt(parts[3], 10, 64)
	if parseErr != nil {
		return "", "", "", time.Time{}, fmt.Errorf("%w: expires: %v", ErrActionTokenMalformed, parseErr)
	}
	expires = time.Unix(expUnix, 0)
	if sig == "" || deviceID == "" || eventID == "" {
		return "", "", "", time.Time{}, ErrActionTokenMalformed
	}
	return sig, deviceID, eventID, expires, nil
}

// VerifyActionToken returns nil if the token is well-formed, not
// expired, and its signature matches the provided per-device secret.
// On success it returns the parsed metadata. Callers should then check
// the action_log for idempotency.
func VerifyActionToken(token string, secret []byte, now time.Time) (ActionToken, error) {
	sig, deviceID, eventID, expires, err := ParseActionToken(token)
	if err != nil {
		return ActionToken{}, err
	}
	if now.After(expires) {
		return ActionToken{}, ErrActionTokenExpired
	}
	expStr := strconv.FormatInt(expires.Unix(), 10)
	payload := deviceID + "|" + eventID + "|" + expStr
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ActionToken{}, fmt.Errorf("%w: signature not base64: %v", ErrActionTokenMalformed, err)
	}
	if !hmac.Equal(got, want) {
		return ActionToken{}, ErrActionTokenBadSignature
	}
	return ActionToken{DeviceID: deviceID, EventID: eventID, Expires: expires}, nil
}
