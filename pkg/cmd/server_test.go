package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cmd/args"
	"github.com/nonchan7720/webhook-over-websocket/pkg/handler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerMemberlist(t *testing.T) {
	c, h, err := newServer(context.Background(), &args.Server{
		Port:                   8080,
		ClusterBackend:         args.ClusterBackendMemberlist,
		MemberListPort:         0,
		MemberlistSyncDuration: time.Second,
	}, "127.0.0.1")
	require.NoError(t, err)
	assert.IsType(t, &cluster.Memberlist{}, c)
	assert.IsType(t, &handler.MemberlistHandler{}, h)
}

func TestNewServerRedis(t *testing.T) {
	c, h, err := newServer(context.Background(), &args.Server{
		Port:                   8080,
		ClusterBackend:         args.ClusterBackendRedis,
		RedisURL:               "rediss://example.com:6379",
		RedisHeartbeatDuration: time.Second,
	}, "127.0.0.1")
	require.NoError(t, err)
	assert.IsType(t, &cluster.Redis{}, c)
	assert.IsType(t, &handler.PubSubHandler{}, h)
}

func TestNewServerRedisInvalidURL(t *testing.T) {
	_, _, err := newServer(context.Background(), &args.Server{
		ClusterBackend: args.ClusterBackendRedis,
		RedisURL:       "://invalid",
	}, "127.0.0.1")
	assert.Error(t, err)
}
