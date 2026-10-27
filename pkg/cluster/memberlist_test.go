package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemberlistImplementsCluster(t *testing.T) {
	var _ PeerCluster = (*Memberlist)(nil)
}

func TestMemberlistBroadcastAndPeers(t *testing.T) {
	const serverPort = 8080
	node1, err := NewMemberlist(MemberlistConfig{NodeName: "node1", BindPort: 0, MyIP: "127.0.0.1", ServerPort: serverPort})
	require.NoError(t, err)
	node2, err := NewMemberlist(MemberlistConfig{NodeName: "node2", BindPort: 0, MyIP: "127.0.0.1", ServerPort: serverPort})
	require.NoError(t, err)

	received := make(chan *NotifyMsg, 1)
	node2.Subscribe(func(msg *NotifyMsg) { received <- msg })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, node1.Start(ctx))
	require.NoError(t, node2.Start(ctx))

	_, err = node1.mlist.Join([]string{node2.mlist.LocalNode().FullAddress().Addr})
	require.NoError(t, err)

	peers, err := node1.Peers(ctx)
	require.NoError(t, err)
	// The advertised IP depends on the host's interfaces, so only the server port is checked.
	require.Len(t, peers, 1)
	assert.Equal(t, net.JoinHostPort(node2.mlist.LocalNode().Addr.String(), "8080"), peers[0])

	require.NoError(t, node1.Broadcast(ctx, &AuthMessage{
		NotifyMsg: NotifyMsg{MsgType: NotifyMsgType_AuthMessage},
		SessionID: testSessionID,
		Token:     testToken,
	}))

	select {
	case msg := <-received:
		auth, err := DecodeNotifyMsg[AuthMessage](msg)
		require.NoError(t, err)
		assert.Equal(t, testSessionID, auth.SessionID)
	case <-time.After(5 * time.Second):
		t.Fatal("node2 did not get the broadcast message")
	}
}
