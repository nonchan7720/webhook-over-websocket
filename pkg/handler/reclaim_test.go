package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testFixedChannelID = "machine-0123456789abcdef"

// newServersOfEachMode starts a server of each cluster mode, keyed by the mode.
func newServersOfEachMode(t *testing.T) map[string]*httptest.Server {
	t.Helper()
	return map[string]*httptest.Server{
		"memberlist": httptest.NewServer(newTestServerHandle(nil).BuildMux()),
		"pubsub":     newPubSubNode(t, miniredis.RunT(t)),
	}
}

func newChannelWithKey(t *testing.T, srv *httptest.Server, channelID, key string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/new?channel_id="+channelID, nil)
	require.NoError(t, err)
	if key != "" {
		req.Header.Set(ChannelKeyHeader, key)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close() //nolint: errcheck
	return resp.StatusCode
}

func dialWithKey(t *testing.T, srv *httptest.Server, channelID, key string) (*websocket.Conn, int) {
	t.Helper()
	header := http.Header{}
	if key != "" {
		header.Set(ChannelKeyHeader, key)
	}
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/"+channelID, header)
	if resp != nil {
		_ = resp.Body.Close() //nolint: errcheck
	}
	if err != nil {
		return nil, resp.StatusCode
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, http.StatusSwitchingProtocols
}

func TestRequestSubjectUsesChannelKeyWithoutAuth(t *testing.T) {
	h := newBase(Options{}, nil)

	r := httptest.NewRequest(http.MethodGet, "/new", nil)
	subject, err := h.requestSubject(r)
	require.NoError(t, err)
	assert.Empty(t, subject, "a client without a key keeps the anonymous subject")

	r.Header.Set(ChannelKeyHeader, "secret-a")
	subjectA, err := h.requestSubject(r)
	require.NoError(t, err)
	assert.NotEmpty(t, subjectA)
	assert.NotContains(t, subjectA, "secret-a", "the key itself must not be stored")

	r.Header.Set(ChannelKeyHeader, "secret-b")
	subjectB, err := h.requestSubject(r)
	require.NoError(t, err)
	assert.NotEqual(t, subjectA, subjectB)
}

func TestNewChannelCanBeReissuedWithTheSameKey(t *testing.T) {
	for name, srv := range newServersOfEachMode(t) {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			require.Equal(t, http.StatusOK, newChannelWithKey(t, srv, testFixedChannelID, "key-a"))
			// e.g. the client restarted before the server noticed the previous run went away.
			assert.Equal(t, http.StatusOK, newChannelWithKey(t, srv, testFixedChannelID, "key-a"))
			assert.Equal(t, http.StatusConflict, newChannelWithKey(t, srv, testFixedChannelID, "key-b"))
			assert.Equal(t, http.StatusConflict, newChannelWithKey(t, srv, testFixedChannelID, ""))
		})
	}
}

func TestNewChannelWithoutKeyCannotBeReissued(t *testing.T) {
	for name, srv := range newServersOfEachMode(t) {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			require.Equal(t, http.StatusOK, newChannelWithKey(t, srv, testFixedChannelID, ""))
			assert.Equal(t, http.StatusConflict, newChannelWithKey(t, srv, testFixedChannelID, ""))
		})
	}
}

func TestWebSocketRequiresTheKeyTheChannelWasIssuedWith(t *testing.T) {
	for name, srv := range newServersOfEachMode(t) {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			require.Equal(t, http.StatusOK, newChannelWithKey(t, srv, testFixedChannelID, "key-a"))
			_, status := dialWithKey(t, srv, testFixedChannelID, "key-b")
			assert.Equal(t, http.StatusForbidden, status)
			_, status = dialWithKey(t, srv, testFixedChannelID, "")
			assert.Equal(t, http.StatusForbidden, status)
			_, status = dialWithKey(t, srv, testFixedChannelID, "key-a")
			assert.Equal(t, http.StatusSwitchingProtocols, status)
		})
	}
}
