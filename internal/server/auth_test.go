package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakshjotwani/gru/internal/server"
)

func TestBearerAuth_missingHeader(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuth_wrongKey(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuth_correctKey(t *testing.T) {
	handler := server.BearerAuth("secret-key", okHandler())
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
