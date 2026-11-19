package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/traefik"
)

// MemberlistHandler serves the tunnel for clusters whose nodes can reach each other directly.
// Traefik routes each channel to the node holding its WebSocket using /traefik-config.
type MemberlistHandler struct {
	base

	myServerURL string
	port        int
	peers       cluster.PeerCluster
}

// NewMemberlistHandler creates the handler and subscribes it to messages from the cluster.
// The cluster must be started by the caller after this.
func NewMemberlistHandler(opts Options, c cluster.PeerCluster) *MemberlistHandler {
	h := &MemberlistHandler{
		base: newBase(opts, c),

		myServerURL: fmt.Sprintf("http://%s:%d", opts.MyIP, opts.Port),
		port:        opts.Port,
		peers:       c,
	}
	c.Subscribe(h.notifyMsg)
	return h
}

// BuildMux constructs the HTTP route table for the server.
func (h *MemberlistHandler) BuildMux() *http.ServeMux {
	mux := http.NewServeMux()
	h.registerRoutes(mux, routes{
		newChannel: h.handleNewChannel,
		webSocket:  h.handleWebSocket,
		webhook:    h.handleWebhook,
	})
	// The HTTP Provider in Traefik periodically checks the configuration output endpoint.
	mux.HandleFunc("GET /traefik-config", h.handleTraefikConfig)
	// Internal endpoint for peers to share information (additional)
	mux.HandleFunc("GET /internal/channels", h.handleInternalChannels)
	return mux
}

func (h *MemberlistHandler) handleNewChannel(w http.ResponseWriter, r *http.Request) {
	// Lock to prevent race condition
	h.activeChannelsMu.Lock()
	defer h.activeChannelsMu.Unlock()
	subject, err := h.requestSubject(r)
	if err != nil {
		http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
		return
	}
	channelID := r.URL.Query().Get("channel_id")
	if channelID == "" {
		channelID = uuid.New().String()
	} else if !validChannelID(channelID) {
		http.Error(w, "Invalid channel_id", http.StatusBadRequest)
		return
	}
	if existing, ok := h.activeChannels[channelID]; ok {
		// The client that issued it can issue it again (e.g. after a restart); keep the entry
		// as is, since it may still hold the previous connection.
		if subject == "" || existing.subject != subject {
			http.Error(w, "Channel ID already exists", http.StatusConflict)
			return
		}
		writeChannelID(w, channelID)
		return
	}
	h.activeChannels[channelID] = &ClientConn{wsConn: nil, subject: subject}
	writeChannelID(w, channelID)
}

func (h *MemberlistHandler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelId")
	if channelID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}
	// When auth is enabled, validate the channel JWT from the Authorization header
	subject, err := h.requestSubject(r)
	if err != nil {
		http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
		return
	}

	h.activeChannelsMu.RLock()
	clientConn, exists := h.activeChannels[channelID]
	h.activeChannelsMu.RUnlock()
	if !exists || clientConn.subject != subject {
		http.Error(w, "Forbidden or invalid channel_id", http.StatusForbidden)
		return
	}

	conn := h.upgradeWebSocket(w, r, clientConn)
	if conn == nil {
		return
	}
	h.serveWebSocket(channelID, clientConn, conn, func(msg TunnelMessage) {
		// Pass the response to the handler waiting for the corresponding ReqID
		h.deliverResponse(msg)
	})
}

func (h *MemberlistHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	channelID := webhookChannelID(r)
	if channelID == "" {
		clientNotConnected(w)
		return
	}
	client := h.localClient(channelID)
	if client == nil {
		clientNotConnected(w)
		return
	}
	h.proxyWebhook(w, r, func(msg TunnelMessage) (int, error) {
		return http.StatusBadGateway, client.send(msg)
	})
}

type InternalChannelsResp struct {
	WsChannels      []string `json:"ws_channels"`
	WebhookChannels []string `json:"webhook_channels"`
	ServerURL       string   `json:"server_url"`
}

func (h *MemberlistHandler) handleInternalChannels(w http.ResponseWriter, r *http.Request) {
	var wsChannels []string
	var webhookChannels []string

	h.activeChannelsMu.RLock()
	for id, client := range h.activeChannels {
		// Create a router for WS even if it is not connected (already issued).
		wsChannels = append(wsChannels, id)
		// Create a router for webhooks only when it is actually connected (isActive).
		if client.connected() {
			webhookChannels = append(webhookChannels, id)
		}
	}
	h.activeChannelsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&InternalChannelsResp{ //nolint: errcheck,errchkjson
		WsChannels:      wsChannels,
		WebhookChannels: webhookChannels,
		ServerURL:       h.myServerURL,
	})
}

func (h *MemberlistHandler) handleTraefikConfig(w http.ResponseWriter, r *http.Request) { //nolint: gocognit
	config := traefik.Config{
		HTTP: traefik.HTTPConfig{
			Routers:  make(map[string]traefik.RouterConfig),
			Services: make(map[string]traefik.ServiceConfig),
		},
	}

	allChannels := make(map[string]InternalChannelsResp) // key: ServerURL

	// First, obtain your own information.
	var myWsChannels []string
	var myWebhookChannels []string

	h.activeChannelsMu.RLock()
	for id, client := range h.activeChannels {
		myWsChannels = append(myWsChannels, id) // All WS items are added.
		if client.connected() {
			myWebhookChannels = append(myWebhookChannels, id) // For webhooks, add only while connected
		}
	}
	h.activeChannelsMu.RUnlock()

	allChannels[h.myServerURL] = InternalChannelsResp{
		WsChannels:      myWsChannels,
		WebhookChannels: myWebhookChannels,
		ServerURL:       h.myServerURL,
	}

	// Gather information on "active peers" detected by the cluster
	peers, err := h.peers.Peers(r.Context())
	if err != nil {
		// Respond with this node's channels only rather than failing the whole config.
		slog.WarnContext(r.Context(), "failed to get cluster peers", slog.String("error", err.Error()))
	}
	if len(peers) > 0 {
		var wg sync.WaitGroup
		infoCh := make(chan InternalChannelsResp, len(peers))

		for _, peer := range peers {
			wg.Add(1)
			go fetchPeerChannels(peer, infoCh, &wg)
		}

		wg.Wait()
		close(infoCh)

		for info := range infoCh {
			// Since my information is the latest in memory, I won't overwrite it.
			if info.ServerURL != h.myServerURL {
				allChannels[info.ServerURL] = info
			}
		}
	}

	// Merge information from all nodes to create JSON for Traefik
	for serverURL, info := range allChannels {
		// First, create the required service definitions uniquely.
		channelSet := make(map[string]bool)
		for _, id := range info.WsChannels {
			channelSet[id] = true
		}
		for _, id := range info.WebhookChannels {
			channelSet[id] = true
		}

		for channelID := range channelSet {
			serviceName := "service-" + channelID
			config.HTTP.Services[serviceName] = traefik.ServiceConfig{
				LoadBalancer: traefik.LoadBalancerConfig{
					Servers: []traefik.ServerConfig{{URL: serverURL}},
				},
			}
		}
		// Webhook connection router (only for connected clients)
		for _, channelID := range info.WebhookChannels {
			webhookRouterName := "webhook-" + channelID
			serviceName := "service-" + channelID

			config.HTTP.Routers[webhookRouterName] = traefik.RouterConfig{
				Rule:    fmt.Sprintf("PathPrefix(`/webhook/%s`)", channelID),
				Service: serviceName,
			}
		}
		// Router for WebSocket connections (all channels, including unconnected ones)
		for _, channelID := range info.WsChannels {
			wsRouterName := "ws-" + channelID
			serviceName := "service-" + channelID

			config.HTTP.Routers[wsRouterName] = traefik.RouterConfig{
				Rule:    fmt.Sprintf("PathPrefix(`/ws/%s`)", channelID),
				Service: serviceName,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = config.ToJSON(w) //nolint: errcheck,errchkjson
}

func fetchPeerChannels(hostPort string, ch chan<- InternalChannelsResp, wg *sync.WaitGroup) {
	defer wg.Done()
	client := http.Client{Timeout: 2 * time.Second} // Keep it brief to avoid making them wait for a response.
	url := fmt.Sprintf("http://%s/internal/channels", hostPort)
	resp, err := client.Get(url)
	if err != nil {
		// Ghost containers and similar cannot be communicated with, so they are ignored.
		return
	}
	defer resp.Body.Close() //nolint: errcheck
	var info InternalChannelsResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err == nil {
		ch <- info
	}
}
