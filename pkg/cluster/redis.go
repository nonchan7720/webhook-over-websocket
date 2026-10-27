package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var _ Relay = (*Redis)(nil)

const (
	defaultRedisKeyPrefix = "webhook-over-websocket"
	// Ownership of a channel expires after missing this many heartbeats (e.g. the node crashed).
	redisOwnerTTLMultiplier = 3
	// An issued channel expires if no WebSocket connects to it within this period.
	redisChannelTTL     = 10 * time.Minute
	redisCleanupTimeout = 5 * time.Second
)

type RedisConfig struct {
	// URL is the connection URL (e.g. redis://user:pass@host:6379/0, rediss:// for TLS).
	URL       string
	KeyPrefix string
	// HeartbeatInterval is how often this node refreshes the ownership of its channels.
	HeartbeatInterval time.Duration
	// ClusterMode uses the Redis Cluster protocol (e.g. cluster-mode-enabled managed Redis).
	ClusterMode bool
	// TLSCAFile is a PEM file of CA certificates to verify the server. Setting it enables TLS.
	TLSCAFile string
	// TLSInsecureSkipVerify disables server certificate verification. Setting it enables TLS.
	TLSInsecureSkipVerify bool
}

// Redis is a Relay implementation. Every node subscribes to a broadcast channel and its own
// inbox channel, and the issued channels and their holders are stored as keys.
type Redis struct {
	client            redis.UniversalClient
	nodeID            string
	prefix            string
	heartbeatInterval time.Duration

	callbackNotifyMsg NotifyMsgFn
	callbackRequest   RelayRequestFn
	callbackResponse  RelayResponseFn

	ownedMu sync.Mutex
	owned   map[string]struct{}
}

func NewRedis(cfg RedisConfig) (*Redis, error) {
	client, err := newRedisClient(cfg)
	if err != nil {
		return nil, err
	}
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = defaultRedisKeyPrefix
	}
	return &Redis{
		client: client,
		// NOTE: Hostnames can collide across clusters (e.g. hot/hot), so a random ID is used.
		nodeID:            uuid.New().String(),
		prefix:            prefix,
		heartbeatInterval: cfg.HeartbeatInterval,
		owned:             make(map[string]struct{}),
	}, nil
}

func (r *Redis) notifyChannel() string             { return r.prefix + ":notify" }
func (r *Redis) inboxChannel(nodeID string) string { return r.prefix + ":node:" + nodeID }
func (r *Redis) channelKey(channelID string) string {
	return r.prefix + ":channel:" + channelID
}
func (r *Redis) ownerKey(channelID string) string { return r.prefix + ":owner:" + channelID }
func (r *Redis) ownerTTL() time.Duration {
	return r.heartbeatInterval * redisOwnerTTLMultiplier
}

func (r *Redis) Start(ctx context.Context) error {
	pubsub := r.client.Subscribe(ctx, r.notifyChannel(), r.inboxChannel(r.nodeID))
	// Wait for the subscriptions to be confirmed so that no message is missed after Start returns.
	for range 2 {
		if _, err := pubsub.Receive(ctx); err != nil {
			_ = pubsub.Close() //nolint: errcheck
			return fmt.Errorf("failed to subscribe redis channel: %w", err)
		}
	}
	go r.receiveLoop(pubsub.Channel())
	go r.heartbeatLoop(ctx, pubsub)
	return nil
}

func (r *Redis) Subscribe(fn NotifyMsgFn)      { r.callbackNotifyMsg = fn }
func (r *Redis) OnRequest(fn RelayRequestFn)   { r.callbackRequest = fn }
func (r *Redis) OnResponse(fn RelayResponseFn) { r.callbackResponse = fn }

func (r *Redis) Broadcast(ctx context.Context, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast message: %w", err)
	}
	return r.publish(ctx, r.notifyChannel(), &redisEnvelope{Kind: envelopeKindNotify, Data: data})
}

func (r *Redis) RegisterChannel(ctx context.Context, channelID, subject string) error {
	if err := r.client.Set(ctx, r.channelKey(channelID), subject, redisChannelTTL).Err(); err != nil {
		return fmt.Errorf("failed to register channel: %w", err)
	}
	return nil
}

func (r *Redis) LookupChannel(ctx context.Context, channelID string) (string, error) {
	subject, err := r.client.Get(ctx, r.channelKey(channelID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrChannelNotFound
	}
	if err != nil {
		return "", fmt.Errorf("failed to lookup channel: %w", err)
	}
	return subject, nil
}

func (r *Redis) ClaimChannel(ctx context.Context, channelID string) error {
	if _, err := r.LookupChannel(ctx, channelID); err != nil {
		return err
	}
	ok, err := r.client.SetNX(ctx, r.ownerKey(channelID), r.nodeID, r.ownerTTL()).Result()
	if err != nil {
		return fmt.Errorf("failed to claim channel: %w", err)
	}
	if !ok {
		owner, err := r.client.Get(ctx, r.ownerKey(channelID)).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("failed to claim channel: %w", err)
		}
		if owner != r.nodeID {
			return ErrChannelInUse
		}
	}
	r.ownedMu.Lock()
	r.owned[channelID] = struct{}{}
	r.ownedMu.Unlock()
	return nil
}

func (r *Redis) UnclaimChannel(ctx context.Context, channelID string) error {
	r.ownedMu.Lock()
	delete(r.owned, channelID)
	r.ownedMu.Unlock()

	owner, err := r.client.Get(ctx, r.ownerKey(channelID)).Result()
	if errors.Is(err, redis.Nil) || (err == nil && owner != r.nodeID) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to unclaim channel: %w", err)
	}
	if err := r.client.Del(ctx, r.ownerKey(channelID)).Err(); err != nil {
		return fmt.Errorf("failed to unclaim channel: %w", err)
	}
	return nil
}

func (r *Redis) ReleaseChannel(ctx context.Context, channelID string) error {
	r.ownedMu.Lock()
	delete(r.owned, channelID)
	r.ownedMu.Unlock()

	owner, err := r.client.Get(ctx, r.ownerKey(channelID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to release channel: %w", err)
	}
	if owner != "" && owner != r.nodeID {
		// The channel has been taken over by another node.
		return nil
	}
	// NOTE: The keys are deleted one by one because they may live in different cluster slots.
	for _, key := range []string{r.ownerKey(channelID), r.channelKey(channelID)} {
		if err := r.client.Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("failed to release channel: %w", err)
		}
	}
	return nil
}

func (r *Redis) ForwardRequest(ctx context.Context, channelID string, data []byte) error {
	owner, err := r.client.Get(ctx, r.ownerKey(channelID)).Result()
	if errors.Is(err, redis.Nil) {
		return ErrChannelNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lookup channel owner: %w", err)
	}
	return r.publish(ctx, r.inboxChannel(owner), &redisEnvelope{Kind: envelopeKindRequest, Channel: channelID, Data: data})
}

func (r *Redis) Reply(ctx context.Context, nodeID string, data []byte) error {
	return r.publish(ctx, r.inboxChannel(nodeID), &redisEnvelope{Kind: envelopeKindResponse, Data: data})
}

func (r *Redis) publish(ctx context.Context, channel string, envelope *redisEnvelope) error {
	envelope.Sender = r.nodeID
	if err := r.client.Publish(ctx, channel, encodeEnvelope(envelope)).Err(); err != nil {
		return fmt.Errorf("failed to publish redis message: %w", err)
	}
	return nil
}

// heartbeat extends the expiry of the channels held by this node.
func (r *Redis) heartbeat(ctx context.Context) error {
	r.ownedMu.Lock()
	channelIDs := make([]string, 0, len(r.owned))
	for id := range r.owned {
		channelIDs = append(channelIDs, id)
	}
	r.ownedMu.Unlock()
	if len(channelIDs) == 0 {
		return nil
	}
	pipe := r.client.Pipeline()
	for _, id := range channelIDs {
		pipe.Expire(ctx, r.ownerKey(id), r.ownerTTL())
		pipe.Expire(ctx, r.channelKey(id), redisChannelTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *Redis) heartbeatLoop(ctx context.Context, pubsub *redis.PubSub) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.shutdown(pubsub)
			return
		case <-ticker.C:
			if err := r.heartbeat(ctx); err != nil {
				slog.Warn("failed to send heartbeat to redis", slog.String("error", err.Error()))
			}
		}
	}
}

func (r *Redis) shutdown(pubsub *redis.PubSub) {
	ctx, cancel := context.WithTimeout(context.Background(), redisCleanupTimeout)
	defer cancel()
	r.ownedMu.Lock()
	channelIDs := make([]string, 0, len(r.owned))
	for id := range r.owned {
		channelIDs = append(channelIDs, id)
	}
	r.ownedMu.Unlock()
	for _, id := range channelIDs {
		if err := r.ReleaseChannel(ctx, id); err != nil {
			slog.Warn("failed to release channel", slog.String("channel-id", id), slog.String("error", err.Error()))
		}
	}
	if err := pubsub.Close(); err != nil {
		slog.Warn("failed to close redis pubsub", slog.String("error", err.Error()))
	}
	if err := r.client.Close(); err != nil {
		slog.Warn("failed to close redis client", slog.String("error", err.Error()))
	}
}

func (r *Redis) receiveLoop(ch <-chan *redis.Message) {
	for m := range ch {
		envelope, err := decodeEnvelope([]byte(m.Payload))
		if err != nil {
			slog.Error(err.Error())
			continue
		}
		if m.Channel == r.notifyChannel() {
			r.handleNotify(envelope)
			continue
		}
		switch envelope.Kind {
		case envelopeKindRequest:
			if r.callbackRequest != nil {
				r.callbackRequest(envelope.Sender, envelope.Channel, envelope.Data)
			}
		case envelopeKindResponse:
			if r.callbackResponse != nil {
				r.callbackResponse(envelope.Data)
			}
		}
	}
}

func (r *Redis) handleNotify(envelope *redisEnvelope) {
	if envelope.Sender == r.nodeID {
		return
	}
	msg, err := parseNotifyMsg(envelope.Data)
	if err != nil {
		slog.Error(err.Error())
		return
	}
	if r.callbackNotifyMsg != nil {
		r.callbackNotifyMsg(msg)
	}
}
