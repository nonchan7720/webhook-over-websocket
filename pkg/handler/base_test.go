package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingRequestDeliverDoesNotBlockAfterWaiterIsDone(t *testing.T) {
	p := newPendingRequest()
	for range cap(p.ch) {
		require.True(t, p.deliver(TunnelMessage{ReqID: "r"}))
	}
	// The webhook handler has returned (e.g. timed out) while a response is still arriving.
	p.finish()

	done := make(chan bool, 1)
	go func() { done <- p.deliver(TunnelMessage{ReqID: "r"}) }()
	select {
	case delivered := <-done:
		assert.False(t, delivered)
	case <-time.After(time.Second):
		t.Fatal("deliver blocked after the waiter finished")
	}
}

func TestDeliverResponseToUnknownRequest(t *testing.T) {
	h := newBase(Options{ServerCtx: context.Background()}, nil)
	assert.False(t, h.deliverResponse(TunnelMessage{ReqID: "unknown"}))
}

func TestNotifyAuthMessageDoesNotBlockWithoutReceiver(t *testing.T) {
	h := newBase(Options{ServerCtx: context.Background()}, nil)
	// The CLI disconnected right after the session was looked up: nobody receives the token.
	h.waitingClients.Store("session-1", make(chan string))

	msg := &cluster.NotifyMsg{MsgType: cluster.NotifyMsgType_AuthMessage, Raw: []byte(`{"msg_type":"auth_message","session_id":"session-1","token":"t"}`)}
	done := make(chan struct{})
	go func() {
		h.notifyMsg(msg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notifyMsg blocked without a receiver")
	}
}

func TestLocalClientDoesNotRaceWithUpgrade(t *testing.T) {
	h := newBase(Options{ServerCtx: context.Background()}, nil)
	client := &ClientConn{}
	h.activeChannels["ch"] = client

	done := make(chan struct{})
	go func() {
		defer close(done)
		// upgradeWebSocket attaches the connection under the client's lock.
		client.mu.Lock()
		client.wsConn = &websocket.Conn{}
		client.attached.Store(true)
		client.mu.Unlock()
	}()
	_ = h.localClient("ch")
	h.CleanNonActiveSession()
	<-done
}

func TestValidChannelID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{id: "0b7f6c1e-1d4a-4c2b-9f00-123456789abc", want: true},
		{id: "my-app_v1.2", want: true},
		{id: strings.Repeat("a", maxChannelIDLength), want: true},
		{id: strings.Repeat("a", maxChannelIDLength+1), want: false},
		{id: "", want: false},
		{id: "a/b", want: false},
		{id: "a b", want: false},
		{id: "a:b", want: false},
		{id: "日本語", want: false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, validChannelID(tt.id), "channel id %q", tt.id)
	}
}

func TestNewChannelRejectsInvalidChannelID(t *testing.T) {
	for name, srv := range newServersOfEachMode(t) {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			resp, err := http.Get(srv.URL + "/new?channel_id=" + url.QueryEscape(strings.Repeat("a", maxChannelIDLength+1)))
			require.NoError(t, err)
			_ = resp.Body.Close() //nolint: errcheck
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

func TestProxyWebhookRejectsTooLargeBody(t *testing.T) {
	h := newBase(Options{ServerCtx: context.Background(), WebhookMaxBodySize: 8}, nil)
	sent := false
	req := httptest.NewRequest(http.MethodPost, "/webhook/ch", strings.NewReader("0123456789"))
	rec := httptest.NewRecorder()
	h.proxyWebhook(rec, req, func(TunnelMessage) (int, error) {
		sent = true
		return http.StatusBadGateway, nil
	})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.False(t, sent, "a too large request must not be forwarded")
}

func TestNewBaseDefaultsWebhookMaxBodySize(t *testing.T) {
	h := newBase(Options{ServerCtx: context.Background()}, nil)
	assert.Equal(t, int64(DefaultWebhookMaxBodySize), h.webhookMaxBodySize)
}

func TestClientConnSendTimesOutWhenPeerDoesNotRead(t *testing.T) {
	orig := wsWriteTimeout
	wsWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { wsWriteTimeout = orig })

	serverConn := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConn <- conn
	}))
	t.Cleanup(srv.Close)
	// The peer connects but never reads.
	peer, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	_ = resp.Body.Close()                  //nolint: errcheck
	t.Cleanup(func() { _ = peer.Close() }) //nolint: errcheck

	client := &ClientConn{wsConn: <-serverConn}
	payload := make([]byte, 1<<20)
	done := make(chan error, 1)
	go func() {
		for {
			if err := client.send(TunnelMessage{ReqID: "r", Payload: payload}); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("send blocked on a peer that does not read")
	}
}

// The deadline set by send() must not break the pings sent later on the same connection:
// a stale deadline makes the ping fail at once and the connection is dropped.
func TestServeWebSocketKeepsPingingAfterSend(t *testing.T) {
	origWrite, origPing := wsWriteTimeout, wsPingInterval
	wsWriteTimeout, wsPingInterval = 100*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { wsWriteTimeout, wsPingInterval = origWrite, origPing })

	h := newBase(Options{ServerCtx: context.Background()}, nil)
	clients := make(chan *ClientConn, 1)
	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc := &ClientConn{}
		conn := h.upgradeWebSocket(w, r, cc)
		if conn == nil {
			return
		}
		clients <- cc
		h.serveWebSocket("ch", cc, conn, func(TunnelMessage) {})
		close(served)
	}))
	t.Cleanup(srv.Close)
	peer, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	_ = resp.Body.Close()                  //nolint: errcheck
	t.Cleanup(func() { _ = peer.Close() }) //nolint: errcheck
	pings := make(chan struct{}, 64)
	peer.SetPingHandler(func(string) error { pings <- struct{}{}; return nil })
	go func() {
		for {
			if _, _, err := peer.ReadMessage(); err != nil {
				return
			}
		}
	}()

	cc := <-clients
	require.NoError(t, cc.send(TunnelMessage{ReqID: "r", Payload: []byte("hello")}))
	// Wait well past the write deadline of send() and several ping intervals.
	deadline := time.After(1 * time.Second)
	received := 0
	for received < 3 {
		select {
		case <-pings:
			received++
		case <-served:
			t.Fatal("the server dropped the connection after a send")
		case <-deadline:
			t.Fatalf("expected at least 3 pings after a send, got %d", received)
		}
	}
	require.NoError(t, cc.send(TunnelMessage{ReqID: "r2", Payload: []byte("hello again")}), "send after pings")
}

func TestWebhookChannelIDRejectsInvalidID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/webhook/my-app_v1.2/hooks/github", want: "my-app_v1.2"},
		{path: "/webhook/" + strings.Repeat("a", maxChannelIDLength), want: strings.Repeat("a", maxChannelIDLength)},
		{path: "/webhook/" + strings.Repeat("a", maxChannelIDLength+1), want: ""},
		{path: "/webhook/", want: ""},
		{path: "/webhook/a%20b", want: ""},
		{path: "/webhook/a:b", want: ""},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, tt.path, nil)
		assert.Equal(t, tt.want, webhookChannelID(req), tt.path)
	}
}
