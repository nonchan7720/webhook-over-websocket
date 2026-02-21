package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nonchan7720/webhook-over-websocket/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("test-secret-key")

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{"valid bearer", "Bearer mytoken123", "mytoken123"},
		{"no header", "", ""},
		{"wrong scheme", "Basic abc", ""},
		{"bearer only", "Bearer ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			assert.Equal(t, tc.expected, BearerToken(r))
		})
	}
}

func TestJWTSession_NoToken(t *testing.T) {
	handler := JWTSession(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/new", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestJWTSession_ValidSessionToken(t *testing.T) {
	token, err := auth.IssueSessionToken(testSecret, "octocat")
	require.NoError(t, err)

	handler := JWTSession(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/new", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestJWTSession_ChannelTokenRejected(t *testing.T) {
	// A channel token must not be accepted by the session middleware
	token, err := auth.IssueChannelToken(testSecret, "some-channel")
	require.NoError(t, err)

	handler := JWTSession(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/new", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestJWTSession_InvalidToken(t *testing.T) {
	handler := JWTSession(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/new", nil)
	req.Header.Set("Authorization", "Bearer notavalidtoken")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
