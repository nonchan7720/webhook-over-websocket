package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/traefik"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCluster struct {
	peers       []string
	peersErr    error
	notifyFn    cluster.NotifyMsgFn
	broadcasted []any
}

var _ cluster.PeerCluster = (*fakeCluster)(nil)

func (f *fakeCluster) Start(context.Context) error             { return nil }
func (f *fakeCluster) Subscribe(fn cluster.NotifyMsgFn)        { f.notifyFn = fn }
func (f *fakeCluster) Peers(context.Context) ([]string, error) { return f.peers, f.peersErr }
func (f *fakeCluster) Broadcast(_ context.Context, msg any) error {
	f.broadcasted = append(f.broadcasted, msg)
	return nil
}

// newTestServerHandle builds a minimal MemberlistHandler (auth disabled).
func newTestServerHandle(c *fakeCluster) *MemberlistHandler {
	if c == nil {
		c = &fakeCluster{}
	}
	return NewMemberlistHandler(Options{
		MyIP:      "127.0.0.1",
		Port:      8080,
		ServerCtx: context.Background(),
	}, c)
}

func TestWebhookRouteAllowsAllHTTPMethods(t *testing.T) {
	h := newTestServerHandle(nil)
	mux := h.BuildMux()

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
	h := newTestServerHandle(nil)
	mux := h.BuildMux()

	req := httptest.NewRequest(http.MethodPost, "/webhook/", nil)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		mux.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTraefikConfigAggregatesPeerChannels(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/internal/channels", r.URL.Path)
		_ = json.NewEncoder(w).Encode(&InternalChannelsResp{ //nolint: errcheck,errchkjson
			WsChannels:      []string{"peer-channel"},
			WebhookChannels: []string{"peer-channel"},
			ServerURL:       "http://10.0.0.2:8080",
		})
	}))
	defer peer.Close()
	peerURL, err := url.Parse(peer.URL)
	require.NoError(t, err)

	h := newTestServerHandle(&fakeCluster{peers: []string{peerURL.Host}})
	rec := httptest.NewRecorder()
	h.BuildMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/traefik-config", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var config traefik.Config
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&config))
	assert.Contains(t, config.HTTP.Routers, "ws-peer-channel")
	assert.Contains(t, config.HTTP.Routers, "webhook-peer-channel")
	require.Contains(t, config.HTTP.Services, "service-peer-channel")
	assert.Equal(t, "http://10.0.0.2:8080", config.HTTP.Services["service-peer-channel"].LoadBalancer.Servers[0].URL)
}

func TestTraefikConfigIgnoresPeersError(t *testing.T) {
	h := newTestServerHandle(&fakeCluster{peersErr: assert.AnError})
	rec := httptest.NewRecorder()
	h.BuildMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/traefik-config", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNotifyAuthMessageDeliversTokenToWaitingClient(t *testing.T) {
	c := &fakeCluster{}
	h := newTestServerHandle(c)
	require.NotNil(t, c.notifyFn, "NewServeHandler must subscribe to cluster messages")

	tokenCh := make(chan string, 1)
	h.waitingClients.Store("session-1", tokenCh)

	raw, err := json.Marshal(&cluster.AuthMessage{
		NotifyMsg: cluster.NotifyMsg{MsgType: cluster.NotifyMsgType_AuthMessage},
		SessionID: "session-1",
		Token:     "token-1",
	})
	require.NoError(t, err)
	c.notifyFn(&cluster.NotifyMsg{MsgType: cluster.NotifyMsgType_AuthMessage, Raw: raw})

	select {
	case token := <-tokenCh:
		assert.Equal(t, "token-1", token)
	case <-time.After(time.Second):
		t.Fatal("token was not delivered to the waiting client")
	}
}

func TestNotifyInvalidAuthMessageDoesNotPanic(t *testing.T) {
	c := &fakeCluster{}
	newTestServerHandle(c)
	assert.NotPanics(t, func() {
		c.notifyFn(&cluster.NotifyMsg{MsgType: cluster.NotifyMsgType_AuthMessage, Raw: []byte("invalid")})
	})
}
