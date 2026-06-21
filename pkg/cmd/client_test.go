package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeRawRequest builds a minimal raw HTTP/1.1 GET request for the given path.
func makeRawRequest(path string) []byte {
	return []byte(fmt.Sprintf("GET %s HTTP/1.1\r\nHost: example.com\r\n\r\n", path))
}

// wsServerPair creates a WebSocket server and returns the client connection and a
// channel that receives TunnelMessages written by handleHTTPRequest.
func wsServerPair(t *testing.T) (*websocket.Conn, <-chan TunnelMessage) {
	t.Helper()
	ch := make(chan TunnelMessage, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close() //nolint:errcheck
		var msg TunnelMessage
		if err := conn.ReadJSON(&msg); err == nil {
			ch <- msg
		}
	}))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck
	return conn, ch
}

func TestHandleHTTPRequestPathForwarding(t *testing.T) {
	tests := []struct {
		name         string
		requestPath  string
		targetPath   string // path suffix for the target URL
		expectedPath string
	}{
		{
			name:         "root path forwarded as root",
			requestPath:  "/",
			targetPath:   "",
			expectedPath: "/",
		},
		{
			name:         "path suffix forwarded to local server",
			requestPath:  "/api/users",
			targetPath:   "",
			expectedPath: "/api/users",
		},
		{
			name:         "path suffix merged with target base path",
			requestPath:  "/users",
			targetPath:   "/api",
			expectedPath: "/api/users",
		},
		{
			name:         "trailing slash preserved",
			requestPath:  "/api/",
			targetPath:   "",
			expectedPath: "/api/",
		},
		{
			name:         "nested path forwarded",
			requestPath:  "/webhooks/github/push",
			targetPath:   "",
			expectedPath: "/webhooks/github/push",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu           sync.Mutex
				receivedPath string
			)
			// Local server simulating the user's application
			localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				receivedPath = r.URL.Path
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer localServer.Close()

			targetURL := "http://" + localServer.Listener.Addr().String() + tc.targetPath

			wsConn, _ := wsServerPair(t)
			var wsMutex sync.Mutex

			msg := TunnelMessage{
				ReqID:   "test-req",
				Payload: makeRawRequest(tc.requestPath),
			}

			handleHTTPRequest(
				context.Background(),
				msg,
				wsConn,
				&wsMutex,
				targetURL,
				5*time.Second,
				false,
			)

			mu.Lock()
			got := receivedPath
			mu.Unlock()
			assert.Equal(t, tc.expectedPath, got)
		})
	}
}

func TestServerPathStripping(t *testing.T) {
	tests := []struct {
		name         string
		fullPath     string
		channelID    string
		expectedPath string
	}{
		{
			name:         "no suffix",
			fullPath:     "/webhook/ch1",
			channelID:    "ch1",
			expectedPath: "/",
		},
		{
			name:         "trailing slash only",
			fullPath:     "/webhook/ch1/",
			channelID:    "ch1",
			expectedPath: "/",
		},
		{
			name:         "single segment suffix",
			fullPath:     "/webhook/ch1/events",
			channelID:    "ch1",
			expectedPath: "/events",
		},
		{
			name:         "nested suffix",
			fullPath:     "/webhook/ch1/api/webhooks/github",
			channelID:    "ch1",
			expectedPath: "/api/webhooks/github",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pathPrefix := "/webhook/" + tc.channelID
			pathSuffix := strings.TrimPrefix(tc.fullPath, pathPrefix)
			if pathSuffix == "" || pathSuffix[0] != '/' {
				pathSuffix = "/" + pathSuffix
			}
			assert.Equal(t, tc.expectedPath, pathSuffix)
		})
	}
}
