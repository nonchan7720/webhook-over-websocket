package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testChannelID = "channel-1"

func newTestRedis(t *testing.T, mr *miniredis.Miniredis) *Redis {
	t.Helper()
	r, err := NewRedis(RedisConfig{
		URL:               "redis://" + mr.Addr(),
		KeyPrefix:         "test",
		HeartbeatInterval: time.Second,
	})
	require.NoError(t, err)
	return r
}

func startTestRedis(t *testing.T, r *Redis) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, r.Start(ctx))
}

type relayRequest struct {
	from      string
	channelID string
	data      []byte
}

func TestRedisImplementsClusterAndRelay(t *testing.T) {
	var _ Cluster = (*Redis)(nil)
	var _ Relay = (*Redis)(nil)
}

func TestNewRedisInvalidURL(t *testing.T) {
	_, err := NewRedis(RedisConfig{URL: "://invalid"})
	assert.Error(t, err)
}

func TestRedisBroadcastDeliversToOtherNodesOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	sender := newTestRedis(t, mr)
	receiver := newTestRedis(t, mr)

	senderCh := make(chan *NotifyMsg, 1)
	receiverCh := make(chan *NotifyMsg, 1)
	sender.Subscribe(func(msg *NotifyMsg) { senderCh <- msg })
	receiver.Subscribe(func(msg *NotifyMsg) { receiverCh <- msg })

	startTestRedis(t, sender)
	startTestRedis(t, receiver)

	err := sender.Broadcast(context.Background(), &AuthMessage{
		NotifyMsg: NotifyMsg{MsgType: NotifyMsgType_AuthMessage},
		SessionID: testSessionID,
		Token:     testToken,
	})
	require.NoError(t, err)

	select {
	case msg := <-receiverCh:
		assert.Equal(t, NotifyMsgType_AuthMessage, msg.MsgType)
		auth, err := DecodeNotifyMsg[AuthMessage](msg)
		require.NoError(t, err)
		assert.Equal(t, testSessionID, auth.SessionID)
		assert.Equal(t, testToken, auth.Token)
	case <-time.After(3 * time.Second):
		t.Fatal("receiver did not get the broadcast message")
	}

	select {
	case <-senderCh:
		t.Fatal("sender must not receive its own broadcast message")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRedisRegisterAndLookupChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, "user-1"))

	// The channel issued on node1 is visible from node2.
	subject, err := node2.LookupChannel(ctx, testChannelID)
	require.NoError(t, err)
	assert.Equal(t, "user-1", subject)

	_, err = node2.LookupChannel(ctx, "unknown")
	assert.ErrorIs(t, err, ErrChannelNotFound)
}

func TestRedisRegisterChannelWithEmptySubject(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	subject, err := r.LookupChannel(ctx, testChannelID)
	require.NoError(t, err)
	assert.Empty(t, subject)
}

func TestRedisForwardRequestAndReply(t *testing.T) {
	mr := miniredis.RunT(t)
	owner := newTestRedis(t, mr)
	receiver := newTestRedis(t, mr)
	ctx := context.Background()

	requests := make(chan relayRequest, 1)
	owner.OnRequest(func(from, channelID string, data []byte) {
		requests <- relayRequest{from: from, channelID: channelID, data: data}
	})
	responses := make(chan []byte, 1)
	receiver.OnResponse(func(data []byte) { responses <- data })
	startTestRedis(t, owner)
	startTestRedis(t, receiver)

	require.NoError(t, owner.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, owner.ClaimChannel(ctx, testChannelID))

	// The node that received the webhook forwards it to the owner of the WebSocket.
	require.NoError(t, receiver.ForwardRequest(ctx, testChannelID, []byte(`{"req_id":"r1"}`)))

	var req relayRequest
	select {
	case req = <-requests:
		assert.Equal(t, testChannelID, req.channelID)
		assert.JSONEq(t, `{"req_id":"r1"}`, string(req.data))
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not get the forwarded request")
	}

	// The owner replies to the node that forwarded the request.
	require.NoError(t, owner.Reply(ctx, req.from, []byte(`{"req_id":"r1","eof":true}`)))
	select {
	case data := <-responses:
		assert.JSONEq(t, `{"req_id":"r1","eof":true}`, string(data))
	case <-time.After(3 * time.Second):
		t.Fatal("receiver did not get the reply")
	}
}

func TestRedisForwardRequestToUnclaimedChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	err := r.ForwardRequest(ctx, testChannelID, []byte(`{}`))
	assert.ErrorIs(t, err, ErrChannelNotFound)
}

func TestRedisClaimChannelConflict(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, node1.ClaimChannel(ctx, testChannelID))
	assert.ErrorIs(t, node2.ClaimChannel(ctx, testChannelID), ErrChannelInUse)
}

func TestRedisClaimUnregisteredChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	assert.ErrorIs(t, r.ClaimChannel(context.Background(), "unknown"), ErrChannelNotFound)
}

func TestRedisReleaseChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, r.ClaimChannel(ctx, testChannelID))
	require.NoError(t, r.ReleaseChannel(ctx, testChannelID))

	_, err := r.LookupChannel(ctx, testChannelID)
	assert.ErrorIs(t, err, ErrChannelNotFound)
	assert.ErrorIs(t, r.ForwardRequest(ctx, testChannelID, []byte(`{}`)), ErrChannelNotFound)
}

func TestRedisOwnershipExpiresWithoutHeartbeat(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr) // not started, so no heartbeat refreshes the ownership
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, r.ClaimChannel(ctx, testChannelID))
	mr.FastForward(time.Minute)

	assert.ErrorIs(t, r.ForwardRequest(ctx, testChannelID, []byte(`{}`)), ErrChannelNotFound)
}

func TestRedisHeartbeatKeepsOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, r.ClaimChannel(ctx, testChannelID))
	for range 10 {
		mr.FastForward(time.Second)
		require.NoError(t, r.heartbeat(ctx))
	}
	require.NoError(t, r.ForwardRequest(ctx, testChannelID, []byte(`{}`)))
}

func TestRedisStopReleasesOwnedChannels(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, node1.Start(ctx))
	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, node1.ClaimChannel(ctx, testChannelID))

	cancel()
	assert.Eventually(t, func() bool {
		_, err := node2.LookupChannel(context.Background(), testChannelID)
		return err != nil
	}, 3*time.Second, 50*time.Millisecond)
}

func TestRedisStartFailsWhenUnreachable(t *testing.T) {
	r, err := NewRedis(RedisConfig{
		URL:               "redis://127.0.0.1:1",
		HeartbeatInterval: time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	assert.Error(t, r.Start(ctx))
}

func TestRedisUnclaimChannelKeepsRegistration(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, "user-1"))
	require.NoError(t, node1.ClaimChannel(ctx, testChannelID))
	require.NoError(t, node1.UnclaimChannel(ctx, testChannelID))

	subject, err := node2.LookupChannel(ctx, testChannelID)
	require.NoError(t, err, "the channel must stay registered")
	assert.Equal(t, "user-1", subject)
	assert.ErrorIs(t, node2.ForwardRequest(ctx, testChannelID, []byte(`{}`)), ErrChannelNotFound)
	// Another node can claim it now.
	require.NoError(t, node2.ClaimChannel(ctx, testChannelID))
}

func TestRedisUnclaimChannelOwnedByAnotherNode(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, node1.ClaimChannel(ctx, testChannelID))
	require.NoError(t, node2.UnclaimChannel(ctx, testChannelID))
	assert.ErrorIs(t, node2.ClaimChannel(ctx, testChannelID), ErrChannelInUse, "node1 must still hold the channel")
}

func TestRedisRegisterChannelTwiceReturnsErrChannelExists(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, "user-1"))
	assert.ErrorIs(t, node2.RegisterChannel(ctx, testChannelID, "user-2"), ErrChannelExists)
	subject, err := node1.LookupChannel(ctx, testChannelID)
	require.NoError(t, err)
	assert.Equal(t, "user-1", subject, "the first registration must be kept")
}

func TestRedisHeartbeatRestoresExpiredOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	r := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, r.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, r.ClaimChannel(ctx, testChannelID))
	// The heartbeat was late (e.g. a long GC pause) and the ownership expired.
	mr.FastForward(time.Minute)
	require.ErrorIs(t, r.ForwardRequest(ctx, testChannelID, []byte(`{}`)), ErrChannelNotFound)

	require.NoError(t, r.heartbeat(ctx))
	assert.NoError(t, r.ForwardRequest(ctx, testChannelID, []byte(`{}`)), "the heartbeat must restore the ownership")
}

func TestRedisHeartbeatDoesNotTakeOverAnotherOwner(t *testing.T) {
	mr := miniredis.RunT(t)
	node1 := newTestRedis(t, mr)
	node2 := newTestRedis(t, mr)
	ctx := context.Background()

	require.NoError(t, node1.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, node1.ClaimChannel(ctx, testChannelID))
	mr.FastForward(time.Minute)
	require.NoError(t, node1.RegisterChannel(ctx, "other-channel", ""), "keep redis alive")
	mr.Set(node1.channelKey(testChannelID), "")
	require.NoError(t, node2.ClaimChannel(ctx, testChannelID))

	require.NoError(t, node1.heartbeat(ctx))
	assert.ErrorIs(t, node1.ClaimChannel(ctx, testChannelID), ErrChannelInUse, "node2 must still hold the channel")
}
