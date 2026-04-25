package server

import (
	"strings"
	"testing"
)

// TestValidateLinkURL_accepts confirms the scheme allowlist accepts all
// expected real-world cases.
func TestValidateLinkURL_accepts(t *testing.T) {
	cases := []string{
		"https://github.com/foo/bar/pull/42",
		"http://example.com/something",
		"https://example.slack.com/archives/C0/p123",
		"mailto:someone@example.com",
		"https://example.com:8443/path?q=1#frag",
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			if err := validateLinkURL(u); err != nil {
				t.Errorf("expected accept, got error: %v", err)
			}
		})
	}
}

// TestValidateLinkURL_rejectsScheme covers the schemes that would let an
// agent slip a script execution / local-file fetch / data-URL phishing
// payload in front of the operator.
func TestValidateLinkURL_rejectsScheme(t *testing.T) {
	cases := []struct {
		url    string
		reason string
	}{
		{"javascript:alert(1)", "javascript scheme"},
		{"data:text/html,<script>alert(1)</script>", "data scheme"},
		{"file:///etc/passwd", "file scheme"},
		{"ftp://example.com/", "ftp scheme"},
		{"vbscript:msgbox(1)", "vbscript scheme"},
		// Mixed-case still rejected because we lowercase before allowlist check.
		{"JaVaScRiPt:alert(1)", "javascript scheme (case)"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			err := validateLinkURL(c.url)
			if err == nil {
				t.Errorf("expected reject for %q, got nil", c.url)
				return
			}
			if !strings.Contains(err.Error(), "not allowed") &&
				!strings.Contains(err.Error(), "scheme") {
				t.Errorf("expected scheme-rejection error, got: %v", err)
			}
		})
	}
}

// TestValidateLinkURL_rejectsLocalNetwork covers the "agent slips in a
// link to my internal admin panel" path. We reject without resolving DNS.
func TestValidateLinkURL_rejectsLocalNetwork(t *testing.T) {
	cases := []string{
		"http://localhost/admin",
		"http://127.0.0.1/admin",
		"http://10.0.0.1/admin",
		"http://192.168.1.1/admin",
		"http://172.16.0.1/admin",
		"http://169.254.1.1/admin", // link-local
		"http://[::1]/admin",
		"http://0.0.0.0/admin",
		"https://localhost.localdomain/admin",
		"https://LOCALHOST/admin", // case
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			if err := validateLinkURL(u); err == nil {
				t.Errorf("expected reject for %q, got nil", u)
			}
		})
	}
}

// TestValidateLinkURL_rejectsMalformed covers obviously broken inputs the
// caller might send by mistake.
func TestValidateLinkURL_rejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not a url",
		"https://", // no host
	}
	for _, u := range cases {
		t.Run(u, func(t *testing.T) {
			if err := validateLinkURL(u); err == nil {
				t.Errorf("expected reject for %q, got nil", u)
			}
		})
	}
}
