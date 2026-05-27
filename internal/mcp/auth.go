package mcp

import (
	"crypto/subtle"
	"net/http"
)

// BearerAuth wraps next so every request must carry
// "Authorization: Bearer <token>". The comparison is constant-time
// (crypto/subtle.ConstantTimeCompare) — timing on the header value
// must not leak which prefix bytes were correct.
//
// Lives in the mcp package, not cmd/mcp, so integration tests for the
// MCP tools can exercise the same auth wrapper that production does.
func BearerAuth(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="voicelog"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
