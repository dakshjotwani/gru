package server

import "net/http"

// CORS wraps a handler and adds cross-origin headers so the web dashboard
// (served on a different port) can reach the gRPC/Connect server.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, Connect-Protocol-Version, "+
					"Connect-Timeout-Ms, Grpc-Timeout, X-Grpc-Web, X-User-Agent, "+
					"X-Gru-Session-ID, X-Gru-Runtime")
			w.Header().Set("Access-Control-Expose-Headers",
				"Content-Type, Connect-Protocol-Version, Grpc-Status, Grpc-Message")
			w.Header().Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
