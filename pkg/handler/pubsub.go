package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
)

const relayTimeout = 5 * time.Second

var badGatewayResponse = []byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

// PubSubHandler serves the tunnel when nodes cannot reach each other directly
// (e.g. replicas spread across clusters). Any node can accept /new, /ws and /webhook;
// a webhook is relayed through the broker to the node holding the channel's WebSocket,
// and the response is relayed back.
type PubSubHandler struct {
	base
	relay cluster.Relay

	// remoteRequests maps a ReqID forwarded from another node to that node's ID.
	remoteRequests sync.Map
}

// NewPubSubHandler creates the handler and registers its callbacks to the relay.
// The relay must be started by the caller after this.
func NewPubSubHandler(opts Options, relay cluster.Relay) *PubSubHandler {
	h := &PubSubHandler{
		base:  newBase(opts, relay),
		relay: relay,
	}
	relay.Subscribe(h.notifyMsg)
	relay.OnRequest(h.onRelayRequest)
	relay.OnResponse(h.onRelayResponse)
	return h
}

func (h *PubSubHandler) BuildMux() *http.ServeMux {
	mux := http.NewServeMux()
	h.registerRoutes(mux, routes{
		newChannel: h.handleNewChannel,
		webSocket:  h.handleWebSocket,
		webhook:    h.handleWebhook,
	})
	return mux
}

func (h *PubSubHandler) handleNewChannel(w http.ResponseWriter, r *http.Request) {
	subject, err := h.requestSubject(r)
	if err != nil {
		http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
		return
	}
	channelID := uuid.New().String()
	if v := r.URL.Query().Get("channel_id"); v != "" {
		_, err := h.relay.LookupChannel(r.Context(), v)
		switch {
		case err == nil:
			http.Error(w, "Channel ID already exists", http.StatusConflict)
			return
		case !errors.Is(err, cluster.ErrChannelNotFound):
			slog.ErrorContext(r.Context(), "failed to lookup channel", slog.String("error", err.Error()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		channelID = v
	}
	if err := h.relay.RegisterChannel(r.Context(), channelID, subject); err != nil {
		slog.ErrorContext(r.Context(), "failed to register channel", slog.String("error", err.Error()))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeChannelID(w, channelID)
}

func (h *PubSubHandler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelId")
	if channelID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}
	subject, err := h.requestSubject(r)
	if err != nil {
		http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
		return
	}
	// The channel may have been issued on another node.
	registered, err := h.relay.LookupChannel(r.Context(), channelID)
	if err != nil || registered != subject {
		if err != nil && !errors.Is(err, cluster.ErrChannelNotFound) {
			slog.ErrorContext(r.Context(), "failed to lookup channel", slog.String("error", err.Error()))
		}
		http.Error(w, "Forbidden or invalid channel_id", http.StatusForbidden)
		return
	}
	if h.localClient(channelID) != nil {
		http.Error(w, "Channel is already in use", http.StatusConflict)
		return
	}
	if err := h.relay.ClaimChannel(r.Context(), channelID); err != nil {
		switch {
		case errors.Is(err, cluster.ErrChannelInUse):
			http.Error(w, "Channel is already in use", http.StatusConflict)
		case errors.Is(err, cluster.ErrChannelNotFound):
			http.Error(w, "Forbidden or invalid channel_id", http.StatusForbidden)
		default:
			slog.ErrorContext(r.Context(), "failed to claim channel", slog.String("error", err.Error()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}

	clientConn := &ClientConn{subject: subject}
	h.activeChannelsMu.Lock()
	h.activeChannels[channelID] = clientConn
	h.activeChannelsMu.Unlock()
	conn := h.upgradeWebSocket(w, r, clientConn)
	if conn == nil {
		h.activeChannelsMu.Lock()
		delete(h.activeChannels, channelID)
		h.activeChannelsMu.Unlock()
		// Keep the channel issued so that the client can connect again.
		h.relayCleanup(channelID, h.relay.UnclaimChannel)
		return
	}
	defer h.relayCleanup(channelID, h.relay.ReleaseChannel)
	h.serveWebSocket(channelID, clientConn, conn, h.onClientMessage)
}

func (h *PubSubHandler) relayCleanup(channelID string, fn func(context.Context, string) error) {
	ctx, cancel := context.WithTimeout(context.Background(), relayTimeout)
	defer cancel()
	if err := fn(ctx, channelID); err != nil {
		slog.Warn("failed to clean up channel", slog.String("channel-id", channelID), slog.String("error", err.Error()))
	}
}

// onClientMessage routes a response from the client to the request waiting on this node,
// or relays it back to the node that forwarded the request.
func (h *PubSubHandler) onClientMessage(msg TunnelMessage) {
	if h.deliverResponse(msg) {
		return
	}
	v, ok := h.remoteRequests.Load(msg.ReqID)
	if !ok {
		return
	}
	from, ok := v.(string)
	if !ok {
		return
	}
	if msg.EOF {
		h.remoteRequests.Delete(msg.ReqID)
	}
	h.reply(from, msg)
}

func (h *PubSubHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	channelID := webhookChannelID(r)
	if channelID == "" {
		http.Error(w, "Client not connected", http.StatusNotFound)
		return
	}
	if client := h.localClient(channelID); client != nil {
		h.proxyWebhook(w, r, func(msg TunnelMessage) (int, error) {
			return http.StatusBadGateway, client.send(msg)
		})
		return
	}
	h.proxyWebhook(w, r, func(msg TunnelMessage) (int, error) {
		err := h.relay.ForwardRequest(r.Context(), channelID, encodeRelayMessage(msg))
		if errors.Is(err, cluster.ErrChannelNotFound) {
			return http.StatusNotFound, err
		}
		return http.StatusBadGateway, err
	})
}

// onRelayRequest passes a webhook forwarded from another node to the client connected to this node.
func (h *PubSubHandler) onRelayRequest(from, channelID string, data []byte) {
	msg, err := decodeRelayMessage(data)
	if err != nil {
		slog.Warn("Failed to decode relayed request", slog.String("error", err.Error()))
		return
	}
	client := h.localClient(channelID)
	if client == nil {
		h.replyBadGateway(from, msg.ReqID)
		return
	}
	h.remoteRequests.Store(msg.ReqID, from)
	// The forwarding node gives up after the timeout, so drop the route even if no EOF arrives.
	time.AfterFunc(webhookResponseTimeout+relayTimeout, func() { h.remoteRequests.Delete(msg.ReqID) })
	if err := client.send(msg); err != nil {
		h.remoteRequests.Delete(msg.ReqID)
		h.replyBadGateway(from, msg.ReqID)
	}
}

// onRelayResponse passes a response relayed from the node holding the WebSocket to the waiting webhook.
func (h *PubSubHandler) onRelayResponse(data []byte) {
	msg, err := decodeRelayMessage(data)
	if err != nil {
		slog.Warn("Failed to decode relayed response", slog.String("error", err.Error()))
		return
	}
	h.deliverResponse(msg)
}

func (h *PubSubHandler) replyBadGateway(to, reqID string) {
	h.reply(to, TunnelMessage{ReqID: reqID, Payload: badGatewayResponse})
	h.reply(to, TunnelMessage{ReqID: reqID, EOF: true})
}

func (h *PubSubHandler) reply(to string, msg TunnelMessage) {
	ctx, cancel := context.WithTimeout(h.serverCtx, relayTimeout)
	defer cancel()
	if err := h.relay.Reply(ctx, to, encodeRelayMessage(msg)); err != nil {
		slog.Warn("Failed to relay response", slog.String("error", err.Error()))
	}
}
