package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// newTestServerHandle builds a minimal serverHandle (auth disabled) with the maps
// that buildMux's registered handlers rely on initialized.
func newTestServerHandle() *serverHandle {
	return &serverHandle{
		activeChannels:  make(map[string]*ClientConn),
		pendingRequests: make(map[string]chan TunnelMessage),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		authEnabled: false,
	}
}

func TestWebhookRouteAllowsAllHTTPMethods(t *testing.T) {
	h := newTestServerHandle()
	mux := h.buildMux()

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/webhook/some-channel-id/any/nested/path", nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			// No client is connected for "some-channel-id", so the handler should
			// report 404, never 405 Method Not Allowed.
			assert.NotEqual(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestWebhookRouteMissingChannelIDReturns404(t *testing.T) {
	h := newTestServerHandle()
	mux := h.buildMux()

	req := httptest.NewRequest(http.MethodPost, "/webhook/", nil)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		mux.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
