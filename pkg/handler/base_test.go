package handler

import (
	"context"
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
