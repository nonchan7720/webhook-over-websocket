package cluster

import (
	"context"
	"errors"
)

var (
	// ErrChannelNotFound is returned when a channel is not issued, or no node holds its WebSocket.
	ErrChannelNotFound = errors.New("channel not found")
	// ErrChannelInUse is returned when another node already holds the WebSocket of the channel.
	ErrChannelInUse = errors.New("channel is already in use")
	// ErrChannelExists is returned when the channel ID has already been issued.
	ErrChannelExists = errors.New("channel already exists")
)

type Cluster interface {
	// Start begins participating in the cluster. Background work stops when ctx is done.
	Start(ctx context.Context) error
	// Subscribe registers the callback for messages broadcast by other nodes.
	// It must be called before Start.
	Subscribe(fn NotifyMsgFn)
	// Broadcast sends msg to all other nodes.
	Broadcast(ctx context.Context, msg any) error
}

// PeerCluster is a cluster whose nodes can reach each other directly over HTTP.
type PeerCluster interface {
	Cluster
	// Peers returns the HTTP addresses (host:port) of the other active nodes.
	Peers(ctx context.Context) ([]string, error)
}

// RelayRequestFn handles a request forwarded from the node `from` to the channel held by this node.
type RelayRequestFn func(from, channelID string, data []byte)

// RelayResponseFn handles a response replied to a request this node forwarded.
type RelayResponseFn func(data []byte)

// Relay is a cluster whose nodes cannot reach each other directly,
// so channel state and all traffic between nodes go through the broker.
type Relay interface {
	Cluster
	// RegisterChannel shares an issued channel so that its WebSocket can connect to any node.
	// It returns ErrChannelExists if the channel ID has already been issued.
	RegisterChannel(ctx context.Context, channelID, subject string) error
	// LookupChannel returns the subject of an issued channel, or ErrChannelNotFound.
	LookupChannel(ctx context.Context, channelID string) (string, error)
	// ClaimChannel marks this node as the holder of the channel's WebSocket.
	// It returns ErrChannelInUse if another node holds it.
	ClaimChannel(ctx context.Context, channelID string) error
	// UnclaimChannel gives up holding the channel but keeps it issued,
	// e.g. when the WebSocket upgrade failed after ClaimChannel.
	UnclaimChannel(ctx context.Context, channelID string) error
	// ReleaseChannel removes the channel after its WebSocket is disconnected.
	ReleaseChannel(ctx context.Context, channelID string) error
	// ForwardRequest sends data to the node holding the channel's WebSocket.
	// It returns ErrChannelNotFound if no node holds it.
	ForwardRequest(ctx context.Context, channelID string, data []byte) error
	// Reply sends data back to the node that forwarded a request.
	Reply(ctx context.Context, nodeID string, data []byte) error
	// OnRequest and OnResponse register the callbacks. They must be called before Start.
	OnRequest(fn RelayRequestFn)
	OnResponse(fn RelayResponseFn)
}
