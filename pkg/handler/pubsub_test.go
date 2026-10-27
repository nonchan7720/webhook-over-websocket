package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPubSubNode starts a PubSubHandler backed by redis and returns its HTTP server.
func newPubSubNode(t *testing.T, mr *miniredis.Miniredis) *httptest.Server {
	t.Helper()
	relay, err := cluster.NewRedis(cluster.RedisConfig{
		URL:               "redis://" + mr.Addr(),
		KeyPrefix:         "test",
		HeartbeatInterval: time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := NewPubSubHandler(Options{ServerCtx: ctx}, relay)
	require.NoError(t, relay.Start(ctx))
	srv := httptest.NewServer(h.BuildMux())
	t.Cleanup(srv.Close)
	return srv
}

func issueChannel(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/new")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint: errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body["channel_id"]
}

func connectWebSocket(t *testing.T, srv *httptest.Server, channelID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/" + channelID
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	_ = resp.Body.Close() //nolint: errcheck
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// serveTunnelClient behaves like the CLI client: it answers every request with the given body.
func serveTunnelClient(conn *websocket.Conn, body string, received chan<- string) {
	for {
		var msg TunnelMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		received <- string(msg.Payload)
		_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, Payload: []byte("HTTP/1.1 201 Created\r\nX-Test: ok\r\n\r\n")}) //nolint: errcheck
		_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, Payload: []byte(body)})                                         //nolint: errcheck
		_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, EOF: true})                                                     //nolint: errcheck
	}
}

func TestPubSubWebhookIsForwardedToNodeHoldingWebSocket(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	// The channel is issued on node A, but the client connects to node B.
	channelID := issueChannel(t, nodeA)
	conn := connectWebSocket(t, nodeB, channelID)
	received := make(chan string, 1)
	go serveTunnelClient(conn, "hello from client", received)

	// The webhook arrives at node A, which does not hold the WebSocket.
	resp, err := http.Post(nodeA.URL+"/webhook/"+channelID+"/events", "application/json", strings.NewReader(`{"a":1}`))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint: errcheck

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "ok", resp.Header.Get("X-Test"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "hello from client", string(body))

	select {
	case raw := <-received:
		assert.Contains(t, raw, "POST /webhook/"+channelID+"/events")
		assert.Contains(t, raw, `{"a":1}`)
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive the webhook")
	}
}

func TestPubSubWebhookOnNodeHoldingWebSocket(t *testing.T) {
	mr := miniredis.RunT(t)
	node := newPubSubNode(t, mr)

	channelID := issueChannel(t, node)
	conn := connectWebSocket(t, node, channelID)
	go serveTunnelClient(conn, "local", make(chan string, 1))

	resp, err := http.Post(node.URL+"/webhook/"+channelID, "text/plain", strings.NewReader("x"))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint: errcheck
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "local", string(body))
}

func TestPubSubWebhookForUnconnectedChannelReturns404(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	channelID := issueChannel(t, nodeA)
	for _, path := range []string{"/webhook/" + channelID, "/webhook/unknown", "/webhook/"} {
		resp, err := http.Post(nodeB.URL+path, "text/plain", strings.NewReader("x"))
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		_ = resp.Body.Close() //nolint: errcheck
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		// Same body as the memberlist mode, so clients see the same behaviour.
		assert.Equal(t, "Client not connected\n", string(body), path)
	}
}

func TestPubSubWebSocketRejectsUnknownChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	node := newPubSubNode(t, mr)

	wsURL := "ws" + strings.TrimPrefix(node.URL, "http") + "/ws/unknown"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	defer resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestPubSubWebSocketRejectsChannelInUseOnAnotherNode(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	channelID := issueChannel(t, nodeA)
	connectWebSocket(t, nodeA, channelID)

	wsURL := "ws" + strings.TrimPrefix(nodeB.URL, "http") + "/ws/" + channelID
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	defer resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestPubSubNewChannelRejectsDuplicateIDOnAnotherNode(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	channelID := issueChannel(t, nodeA)
	resp, err := http.Get(nodeB.URL + "/new?channel_id=" + channelID)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestPubSubDoesNotServeTraefikConfig(t *testing.T) {
	mr := miniredis.RunT(t)
	node := newPubSubNode(t, mr)

	for _, path := range []string{"/traefik-config", "/internal/channels"} {
		resp, err := http.Get(node.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close() //nolint: errcheck
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}

func TestPubSubLargeResponseIsRelayedIntact(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	channelID := issueChannel(t, nodeA)
	conn := connectWebSocket(t, nodeB, channelID)
	// Compressible and large enough to be split into chunks by the client and compressed in redis.
	body := strings.Repeat(`{"key":"value","n":12345}`, 40*1024)
	go func() {
		var msg TunnelMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, Payload: []byte("HTTP/1.1 200 OK\r\n\r\n")}) //nolint: errcheck
		for chunk := range slices.Chunk([]byte(body), 32*1024) {
			_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, Payload: chunk}) //nolint: errcheck
		}
		_ = conn.WriteJSON(TunnelMessage{ReqID: msg.ReqID, EOF: true}) //nolint: errcheck
	}()

	resp, err := http.Post(nodeA.URL+"/webhook/"+channelID, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint: errcheck
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, len(body), len(got))
	assert.Equal(t, body, string(got))
}

func TestPubSubWebSocketWithoutUpgradeKeepsChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	node := newPubSubNode(t, mr)
	channelID := issueChannel(t, node)

	// A plain HTTP request to /ws must be rejected without dropping the issued channel.
	resp, err := http.Get(node.URL + "/ws/" + channelID)
	require.NoError(t, err)
	_ = resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, err = http.Get(node.URL + "/new?channel_id=" + channelID)
	require.NoError(t, err)
	_ = resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "the channel must still be registered")

	connectWebSocket(t, node, channelID)
}

func TestPubSubPlainRequestToChannelInUseOnAnotherNodeReturns409(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := newPubSubNode(t, mr)
	nodeB := newPubSubNode(t, mr)

	channelID := issueChannel(t, nodeA)
	connectWebSocket(t, nodeB, channelID)

	// Same order of checks as the memberlist mode: "in use" is reported before the upgrade check.
	resp, err := http.Get(nodeA.URL + "/ws/" + channelID)
	require.NoError(t, err)
	_ = resp.Body.Close() //nolint: errcheck
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}
