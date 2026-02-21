package middlewares

import (
	"net/http"
	"strings"

	"github.com/nonchan7720/webhook-over-websocket/pkg/auth"
)

// BearerToken extracts the Bearer token from the Authorization header.
func BearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

// JWTSession is a middleware that requires a valid session JWT in the Authorization header.
func JWTSession(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := BearerToken(r)
			if token == "" {
				http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
				return
			}
			claims, err := auth.ParseToken(secret, token)
			if err != nil || claims.TokenType != auth.TokenTypeSession {
				http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
