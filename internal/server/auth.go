package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth wraps handler, requiring a valid Bearer token.
// Uses constant-time comparison to prevent timing attacks.
func BearerAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return parts[1]
	}
	return ""
}
