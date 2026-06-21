package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/nonchan7720/webhook-over-websocket/pkg/auth"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cmd/args"
	"github.com/nonchan7720/webhook-over-websocket/pkg/middlewares"
	"github.com/nonchan7720/webhook-over-websocket/pkg/traefik"
	"github.com/nonchan7720/webhook-over-websocket/pkg/utils"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type serverHandle struct {
	activeChannels   map[string]*ClientConn
	activeChannelsMu sync.RWMutex
	pendingRequests  map[string]chan []byte
	pendingMu        sync.RWMutex
	upgrader         websocket.Upgrader
	myIP             string
	waitingClients   sync.Map

	myServerURL string
	peerDomain  string
	port        int
	serverCtx   context.Context

	mlist *cluster.Memberlist

	authEnabled        bool
	jwtSecret          []byte
	githubClientID     string
	githubClientSecret string
	githubOrg          string
}

func serverCommand() *cobra.Command {
	var args args.Server
	cmd := &cobra.Command{
		Use: "server",
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := viper.BindPFlags(cmd.Flags()); err != nil {
				return err
			}
			if err := viper.Unmarshal(&args); err != nil {
				return err
			}
			level, err := utils.ParseLevel(args.LogLevel)
			if err != nil {
				return err
			}
			var slogHandler slog.Handler
			switch strings.ToLower(args.LogFormat) {
			case "json":
				slogHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
					Level:     level.Level(),
					AddSource: true,
					ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
						if a.Key == "time" { //nolint
							return slog.String(a.Key, time.Now().Format(time.RFC3339))
						}
						return a
					},
				})
			default:
				slogHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
					Level:     level.Level(),
					AddSource: true,
					ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
						if a.Key == "time" { //nolint
							return slog.String(a.Key, time.Now().Format(time.RFC3339))
						}
						return a
					},
				})
			}
			slog.SetDefault(slog.New(slogHandler))
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeServer(cmd.Context(), &args)
		},
	}
	flag := cmd.Flags()
	args.BindFlags(flag)
	return cmd
}

func executeServer(ctx context.Context, args *args.Server) error { //nolint: cyclop
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	// Validate auth configuration
	if err := args.Validate(); err != nil {
		return err
	}
	myIP := getLocalIP()
	authEnabled := args.AuthEnabled()
	handler := &serverHandle{
		myIP:            myIP,
		activeChannels:  make(map[string]*ClientConn),
		pendingRequests: make(map[string]chan []byte),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},

		peerDomain:  args.PeerDomain,
		myServerURL: fmt.Sprintf("http://%s:%d", myIP, args.Port),
		port:        args.Port,
		serverCtx:   serverCtx,

		authEnabled:        authEnabled,
		jwtSecret:          []byte(args.JwtSigningKey),
		githubClientID:     args.GithubClientID,
		githubClientSecret: args.GithubClientSecret,
		githubOrg:          args.GithubOrg,
	}

	mlist, err := cluster.SetUp(args.MemberListPort, myIP, handler.notifyMsg)
	if err != nil {
		return err
	}
	mlist.Start(ctx, args.PeerDomain, args.MemberlistSyncDuration)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()

	// Endpoint for clients to generate channelId upon startup
	if authEnabled {
		mux.Handle("GET /new", middlewares.JWTSession(handler.jwtSecret)(http.HandlerFunc(handler.handleNewChannel)))
		mux.HandleFunc("GET /auth/client", handler.handleWaitHandler)
		mux.HandleFunc("GET /auth/login", handler.handleAuthGitHub)
		mux.HandleFunc("GET /auth/callback", handler.handleAuthCallback)
		// Waiting for WebSocket connections from clients
		mux.Handle("/ws/{channelId}", middlewares.JWTSession(handler.jwtSecret)(http.HandlerFunc(handler.handleWebSocket)))
	} else {
		mux.HandleFunc("GET /new", handler.handleNewChannel)
		// Waiting for WebSocket connections from clients
		mux.HandleFunc("/ws/{channelId}", handler.handleWebSocket)
	}
	// The HTTP Provider in Traefik periodically checks the configuration output endpoint.
	mux.HandleFunc("GET /traefik-config", handler.handleTraefikConfig)
	// Internal endpoint for peers to share information (additional)
	mux.HandleFunc("GET /internal/channels", handler.handleInternalChannels)
	// External webhook reception point via Traefik
	mux.HandleFunc("POST /webhook/", handler.handleWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"OK"}`)) //nolint:errcheck
	})
	skipper := func(r *http.Request) bool {
		switch r.URL.Path {
		case "/healthz":
			fallthrough
		case "/traefik-config":
			fallthrough
		case "/internal/channels":
			return true
		default:
			return false
		}
	}
	srv := http.Server{
		Handler:           middlewares.Logging(skipper)(mux),
		ReadHeaderTimeout: 20 * time.Second,
	}
	slog.Info(fmt.Sprintf("Server listening on :%d", args.Port))
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("failed to run server", slog.String("error", err.Error()))
		}
	}()
	go func() {
		ticker := time.NewTicker(args.CleanupDuration)
		select {
		case <-ticker.C:
			handler.cleanNonActiveSession()
		case <-ctx.Done():
			return
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down server...")

	// Notify all WebSocket connections of shutdown
	serverCancel()

	// Wait a moment, then force quit.
	tCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	slog.InfoContext(tCtx, "Stop server")
	defer cancel()
	return srv.Shutdown(tCtx)
}

type TunnelMessage struct {
	ReqID   string `json:"req_id"`
	Payload []byte `json:"payload"`
}

type ClientConn struct {
	wsConn *websocket.Conn
	mu     sync.Mutex // To prevent simultaneous writes to WebSocket

	subject string
}

func (c *ClientConn) isActive() bool {
	return c.wsConn != nil
}

func (h *serverHandle) handleNewChannel(w http.ResponseWriter, r *http.Request) {
	// Lock to prevent race condition
	h.activeChannelsMu.Lock()
	defer h.activeChannelsMu.Unlock()
	channelID := uuid.New().String()
	if v:= r.URL.Query().Get("channel_id"); v!= "" {
		_, ok := h.activeChannels[v]
		if  ok {
			http.Error(w, "Channel ID already exists", http.StatusConflict)
			return
		}
		channelID = v
	}
	clientConn := &ClientConn{wsConn: nil}
	if h.authEnabled {
		claims, err := auth.ToContext(r.Context())
		if err != nil {
			http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
			return
		}
		clientConn.subject = claims.Subject
	}
	h.activeChannels[channelID] = clientConn
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"channel_id": channelID}
	_ = json.NewEncoder(w).Encode(resp) //nolint: errcheck,errchkjson
	slog.Info("new Channel ID has been issued", slog.String("channel-id", channelID))
}

type InternalChannelsResp struct {
	WsChannels      []string `json:"ws_channels"`
	WebhookChannels []string `json:"webhook_channels"`
	ServerURL       string   `json:"server_url"`
}

func (h *serverHandle) handleWaitHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Create a channel to receive tokens and register it in the map.
	tokenChan := make(chan string)
	h.waitingClients.Store(sessionID, tokenChan)
	defer h.waitingClients.Delete(sessionID)

	// Set the header for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	// Wait until the token arrives or the client disconnects.
	select {
	case token := <-tokenChan:
		// When a token is received, send it to the CLI.
		fmt.Fprintf(w, "data: %s\n\n", token) //nolint:errcheck
		flusher.Flush()
	case <-r.Context().Done():
		// If the CLI side disconnects due to a timeout or similar
		return
	}
}

func (h *serverHandle) handleAuthGitHub(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	state, err := auth.GenerateOAuthState(h.jwtSecret, sessionID)
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	redirectURI := fmt.Sprintf("%s://%s/auth/callback", scheme, r.Host)
	redirectURL := auth.GithubAuthURL(h.githubClientID, state, redirectURI, h.githubOrg != "")
	http.Redirect(w, r, redirectURL, http.StatusFound) //nolint:gosec
}

func (h *serverHandle) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	sessionID, err := auth.ValidateOAuthState(h.jwtSecret, state)
	if err != nil {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	accessToken, err := auth.ExchangeCodeForToken(r.Context(), h.githubClientID, h.githubClientSecret, code)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to exchange GitHub code", slog.String("error", err.Error()))
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	// Check organization membership if an org is configured
	if h.githubOrg != "" {
		member, err := auth.CheckOrgMembership(r.Context(), accessToken, h.githubOrg)
		if err != nil {
			slog.ErrorContext(r.Context(), "failed to check org membership", slog.String("error", err.Error()))
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		if !member {
			http.Error(w, "Forbidden: not a member of the required organization", http.StatusForbidden)
			return
		}
	}

	username, err := auth.GetUsername(r.Context(), accessToken)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to get GitHub username", slog.String("error", err.Error()))
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	sessionToken, err := auth.IssueSessionToken(h.jwtSecret, username)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to issue session token", slog.String("error", err.Error()))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	msg := &cluster.AuthMessage{
		NotifyMsg: cluster.NotifyMsg{
			MsgType: cluster.NotifyMsgType_AuthMessage,
		},
		SessionID: sessionID,
		Token:     sessionToken,
	}
	cluster.SendBroadcastQueue(msg)
	if ch, ok := h.waitingClients.Load(sessionID); ok {
		ch.(chan string) <- sessionToken //nolint: errcheck,forcetypeassert
	}
	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`Login complete. <br />Please close your browser.`)) //nolint: errcheck,gosec
	slog.InfoContext(r.Context(), "user authenticated", slog.String("username", username))
}

func (h *serverHandle) handleInternalChannels(w http.ResponseWriter, r *http.Request) {
	var wsChannels []string
	var webhookChannels []string

	h.activeChannelsMu.RLock()
	for id, client := range h.activeChannels {
		// Create a router for WS even if it is not connected (already issued).
		wsChannels = append(wsChannels, id)
		// Create a router for webhooks only when it is actually connected (isActive).
		if client.isActive() {
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

func (h *serverHandle) handleTraefikConfig(w http.ResponseWriter, r *http.Request) { //nolint: gocognit
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
		if client.isActive() {
			myWebhookChannels = append(myWebhookChannels, id) // For webhooks, add only while connected
		}
	}
	h.activeChannelsMu.RUnlock()

	allChannels[h.myServerURL] = InternalChannelsResp{
		WsChannels:      myWsChannels,
		WebhookChannels: myWebhookChannels,
		ServerURL:       h.myServerURL,
	}

	// Gather information on "active peers" detected by memberlist
	if nodes := h.mlist.ActiveNodesWithoutSelf(); len(nodes) > 0 { //nolint: nestif
		var wg sync.WaitGroup
		infoCh := make(chan InternalChannelsResp, len(nodes))

		for _, node := range nodes {
			wg.Add(1)
			go fetchPeerChannels(
				net.JoinHostPort(node.Addr.String(), fmt.Sprintf("%d", h.port)),
				infoCh,
				&wg,
			)
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

func (h *serverHandle) handleWebSocket(w http.ResponseWriter, r *http.Request) { //nolint: gocognit,cyclop
	channelID := r.PathValue("channelId")
	if channelID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}
	// When auth is enabled, validate the channel JWT from the Authorization header
	subject := ""
	if h.authEnabled {
		claims, err := auth.ToContext(r.Context())
		if err != nil {
			http.Error(w, "Unauthorized: missing authorization token", http.StatusUnauthorized)
			return
		}
		subject = claims.Subject
	}

	h.activeChannelsMu.RLock()
	clientConn, exists := h.activeChannels[channelID]
	h.activeChannelsMu.RUnlock()
	if !exists || clientConn.subject != subject {
		http.Error(w, "Forbidden or invalid channel_id", http.StatusForbidden)
		return
	}

	clientConn.mu.Lock()
	if clientConn.isActive() {
		clientConn.mu.Unlock()
		http.Error(w, "Channel is already in use", http.StatusConflict)
		return
	}
	// The upgrade process causes network I/O waits, so unlock it.
	clientConn.mu.Unlock()
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "Upgrade error", slog.String("error", err.Error()))
		return
	}
	// After the upgrade succeeds, unlock it again and store it
	// final confirmation that it hasn't been intercepted in the meantime.
	clientConn.mu.Lock()
	if clientConn.isActive() {
		clientConn.mu.Unlock()
		_ = conn.WriteMessage( //nolint: errcheck
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Channel is already in use"),
		)
		_ = conn.Close() //nolint: errcheck
		return
	}
	clientConn.wsConn = conn
	clientConn.mu.Unlock()

	slog.Info(fmt.Sprintf("Client connected: %s", channelID)) //nolint:gosec

	// Set the handler for Ping/Pong processing
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // nolint: errcheck
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // nolint: errcheck

	done := make(chan struct{})
	defer func() {
		h.activeChannelsMu.Lock()
		delete(h.activeChannels, channelID)
		h.activeChannelsMu.Unlock()
		_ = conn.Close() //nolint: errcheck
		slog.Info(fmt.Sprintf("Client disconnected: %s", channelID)) //nolint:gosec
	}()

	// Goroutine that periodically sends pings
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				clientConn.mu.Lock()
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					clientConn.mu.Unlock()
					return
				}
				clientConn.mu.Unlock()
			case <-done:
				return
			}
		}
	}()

	// Goroutine receiving client responses from WebSocket
	go func() {
		defer close(done)
		for {
			msgType, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}

			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}

			var msg TunnelMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				slog.Warn("Failed to unmarshal tunnel message", slog.String("error", err.Error()))
				continue
			}

			// Pass the response to the handler waiting for the corresponding ReqID
			h.pendingMu.RLock()
			respCh, exists := h.pendingRequests[msg.ReqID]
			h.pendingMu.RUnlock()

			if exists {
				respCh <- msg.Payload
			}
		}
	}()

	// Wait for a goroutine to terminate or for shutdown
	select {
	case <-done:
		// Normal disconnection
	case <-h.serverCtx.Done():
		// Server Shutdown
		slog.Info(fmt.Sprintf("Closing WebSocket connection due to server shutdown: %s", channelID)) //nolint:gosec
		_ = conn.WriteMessage( //nolint: errcheck
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "Server is shutting down"),
		)
		_ = conn.Close() //nolint: errcheck
	}
}

func (h *serverHandle) handleWebhook(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/webhook/"), "/")
	channelID := parts[0]

	h.activeChannelsMu.RLock()
	client, exists := h.activeChannels[channelID]
	h.activeChannelsMu.RUnlock()

	if !exists || client.wsConn == nil {
		http.Error(w, "Client not connected", http.StatusNotFound)
		return
	}

	// Convert HTTP requests directly into raw byte sequences (equivalent to TCP dumps)
	rawReqBytes, err := httputil.DumpRequest(r, true)
	if err != nil {
		http.Error(w, "Error dumping request", http.StatusInternalServerError)
		return
	}

	reqID := uuid.New().String()
	respCh := make(chan []byte)

	h.pendingMu.Lock()
	h.pendingRequests[reqID] = respCh
	h.pendingMu.Unlock()

	defer func() {
		h.pendingMu.Lock()
		delete(h.pendingRequests, reqID)
		h.pendingMu.Unlock()
	}()

	msg := TunnelMessage{ReqID: reqID, Payload: rawReqBytes}
	client.mu.Lock()
	err = client.wsConn.WriteJSON(msg)
	client.mu.Unlock()

	if err != nil {
		http.Error(w, "Failed to send to client", http.StatusBadGateway)
		return
	}

	// Waiting for a response from the client
	select {
	case rawRespBytes := <-respCh:
		// Restore the raw byte array to an http.Response object
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(rawRespBytes)), r)
		if err != nil {
			http.Error(w, "Bad gateway response from client", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close() //nolint: errcheck,errchkjson
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body) //nolint: errcheck

	case <-time.After(30 * time.Second):
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
	}
}

const localhost = "127.0.0.1"

func getLocalIP() string {
	if podIP := getLocalIPFromPOD_IPEnv(); podIP != "" {
		return podIP
	}

	if candidateIP := getCandidateIP(); candidateIP != "" {
		slog.Info(fmt.Sprintf("Using candidate IP: %s", candidateIP))
		return candidateIP
	}

	return localhost
}

func getLocalIPFromPOD_IPEnv() string {
	if podIP := os.Getenv("POD_IP"); podIP != "" {
		if ip := net.ParseIP(podIP); ip != nil && ip.To4() != nil {
			slog.Info(fmt.Sprintf("Using POD_IP from environment: %s", podIP)) //nolint:gosec
			return podIP
		}
	}
	return ""
}

func getCandidateIP() string { //nolint: gocognit
	interfaces, err := net.Interfaces()
	if err != nil {
		return localhost
	}
	var candidateIP string
	preferredNames := []string{"eth0", "ens", "enp"}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				ip := ipnet.IP.String()

				// If it matches the priority interface name, return immediately.
				for _, prefix := range preferredNames {
					if len(iface.Name) >= len(prefix) && iface.Name[:len(prefix)] == prefix {
						slog.Info(fmt.Sprintf("Using IP from %s: %s", iface.Name, ip))
						return ip
					}
				}

				// Keep as a candidate
				if candidateIP == "" {
					candidateIP = ip
				}
			}
		}
	}

	if candidateIP != "" {
		slog.Info(fmt.Sprintf("Using candidate IP: %s", candidateIP))
		return candidateIP
	}

	return ""
}

func (h *serverHandle) cleanNonActiveSession() {
	h.activeChannelsMu.RLock()
	nonActiveSession := make([]string, 0, len(h.activeChannels))
	for id, client := range h.activeChannels {
		if !client.isActive() {
			nonActiveSession = append(nonActiveSession, id)
		}
	}
	h.activeChannelsMu.RUnlock()
	if len(nonActiveSession) == 0 {
		return
	}
	h.activeChannelsMu.Lock()
	defer h.activeChannelsMu.Unlock()
	for _, id := range nonActiveSession {
		delete(h.activeChannels, id)
	}
}

func (h *serverHandle) notifyMsg(notifyMsg *cluster.NotifyMsg) {
	switch notifyMsg.MsgType {
	case cluster.NotifyMsgType_AuthMessage:
		msg, err := cluster.DecodeNotifyMsg[cluster.AuthMessage](notifyMsg)
		if err != nil {
			slog.Error(err.Error())
		}
		if ch, ok := h.waitingClients.Load(msg.SessionID); ok {
			if c, ok := ch.(chan string); ok {
				c <- msg.Token
			}
		}
	}
}
