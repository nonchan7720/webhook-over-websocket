package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/nonchan7720/webhook-over-websocket/pkg/auth"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/middlewares"
)

const (
	webhookResponseTimeout = 30 * time.Second
	// DefaultWebhookMaxBodySize is the default limit of a webhook request body.
	DefaultWebhookMaxBodySize = 10 << 20
	// maxChannelIDLength keeps channel IDs well below the size limits of the relay envelope.
	maxChannelIDLength = 128
)

// wsWriteTimeout bounds a write to a client, so that a client not reading its socket
// cannot block the caller (e.g. the loop receiving relayed requests) indefinitely.
// NOTE: The deadline set on a gorilla connection stays in effect for every later write,
// so each write must set its own deadline; a write reusing a stale deadline fails at once.
var wsWriteTimeout = 10 * time.Second

// wsPingInterval is how often the server pings a client to keep the connection alive.
var wsPingInterval = 15 * time.Second

// validChannelID reports whether id can be used as a channel ID:
// 1 to maxChannelIDLength characters of [A-Za-z0-9._-].
func validChannelID(id string) bool {
	if id == "" || len(id) > maxChannelIDLength {
		return false
	}
	for _, c := range id {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

type TunnelMessage struct {
	ReqID   string `json:"req_id"`
	Payload []byte `json:"payload,omitempty"`
	EOF     bool   `json:"eof,omitempty"`
}

type ClientConn struct {
	wsConn *websocket.Conn
	mu     sync.Mutex // To prevent simultaneous writes to WebSocket
	// attached mirrors wsConn != nil so that it can be read without waiting for a write holding mu.
	attached atomic.Bool

	subject string
}

// isActive must be called while holding c.mu.
func (c *ClientConn) isActive() bool {
	return c.wsConn != nil
}

// connected reports whether the WebSocket is attached. It is safe to call without holding c.mu.
func (c *ClientConn) connected() bool {
	return c.attached.Load()
}

type Options struct {
	MyIP string
	Port int
	// ServerCtx is canceled when the server shuts down, to notify WebSocket connections.
	ServerCtx context.Context
	// WebhookMaxBodySize limits the webhook request body in bytes. DefaultWebhookMaxBodySize is used if 0.
	WebhookMaxBodySize int64

	AuthEnabled        bool
	JwtSecret          []byte
	GithubClientID     string
	GithubClientSecret string
	GithubOrg          string
}

// base holds the state and handlers shared by every cluster mode.
type base struct {
	activeChannels   map[string]*ClientConn
	activeChannelsMu sync.RWMutex
	pendingRequests  map[string]*pendingRequest
	pendingMu        sync.RWMutex
	upgrader         websocket.Upgrader
	waitingClients   sync.Map
	serverCtx        context.Context

	webhookMaxBodySize int64

	cluster cluster.Cluster

	authEnabled        bool
	jwtSecret          []byte
	githubClientID     string
	githubClientSecret string
	githubOrg          string
}

func newBase(opts Options, c cluster.Cluster) base {
	if opts.WebhookMaxBodySize <= 0 {
		opts.WebhookMaxBodySize = DefaultWebhookMaxBodySize
	}
	return base{
		activeChannels:  make(map[string]*ClientConn),
		pendingRequests: make(map[string]*pendingRequest),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		serverCtx:          opts.ServerCtx,
		webhookMaxBodySize: opts.WebhookMaxBodySize,

		cluster: c,

		authEnabled:        opts.AuthEnabled,
		jwtSecret:          opts.JwtSecret,
		githubClientID:     opts.GithubClientID,
		githubClientSecret: opts.GithubClientSecret,
		githubOrg:          opts.GithubOrg,
	}
}

type routes struct {
	newChannel http.HandlerFunc
	webSocket  http.HandlerFunc
	webhook    http.HandlerFunc
}

// registerRoutes registers the routes shared by every cluster mode, including the
// auth-enabled/disabled branch for /new and /ws/{channelId}.
func (h *base) registerRoutes(mux *http.ServeMux, rt routes) {
	// Endpoint for clients to generate channelId upon startup
	if h.authEnabled {
		mux.Handle("GET /new", middlewares.JWTSession(h.jwtSecret)(rt.newChannel))
		mux.HandleFunc("GET /auth/client", h.handleWaitHandler)
		mux.HandleFunc("GET /auth/login", h.handleAuthGitHub)
		mux.HandleFunc("GET /auth/callback", h.handleAuthCallback)
		// Waiting for WebSocket connections from clients
		mux.Handle("/ws/{channelId}", middlewares.JWTSession(h.jwtSecret)(rt.webSocket))
	} else {
		mux.HandleFunc("GET /new", rt.newChannel)
		// Waiting for WebSocket connections from clients
		mux.HandleFunc("/ws/{channelId}", rt.webSocket)
	}
	// External webhook reception point. All HTTP methods are forwarded (ngrok-like).
	mux.HandleFunc("/webhook/", rt.webhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"OK"}`)) //nolint:errcheck
	})
}

// ChannelKeyHeader carries a secret of the client when auth is disabled. The channel is bound to it,
// so that only the same client can issue the channel again (e.g. after a restart) or connect to it.
const ChannelKeyHeader = "X-Channel-Key"

// requestSubject returns the subject of the authenticated user. When auth is disabled, it returns
// the hash of the channel key, or "" for a client without a key.
func (h *base) requestSubject(r *http.Request) (string, error) {
	if !h.authEnabled {
		key := r.Header.Get(ChannelKeyHeader)
		if key == "" {
			return "", nil
		}
		sum := sha256.Sum256([]byte(key))
		return "key:" + hex.EncodeToString(sum[:]), nil
	}
	claims, err := auth.ToContext(r.Context())
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}

func writeChannelID(w http.ResponseWriter, channelID string) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"channel_id": channelID}
	_ = json.NewEncoder(w).Encode(resp) //nolint: errcheck,errchkjson
	slog.Info("new Channel ID has been issued", slog.String("channel-id", channelID))
}

// localClient returns the client connected to this node, or nil.
func (h *base) localClient(channelID string) *ClientConn {
	h.activeChannelsMu.RLock()
	defer h.activeChannelsMu.RUnlock()
	client, exists := h.activeChannels[channelID]
	if !exists || !client.connected() {
		return nil
	}
	return client
}

func (c *ClientConn) send(msg TunnelMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsConn == nil {
		return fmt.Errorf("client is not connected")
	}
	if err := c.wsConn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return err
	}
	return c.wsConn.WriteJSON(msg)
}

// upgradeWebSocket upgrades the request and attaches the connection to clientConn.
// It returns nil when the response has already been written.
func (h *base) upgradeWebSocket(w http.ResponseWriter, r *http.Request, clientConn *ClientConn) *websocket.Conn {
	clientConn.mu.Lock()
	if clientConn.isActive() {
		clientConn.mu.Unlock()
		http.Error(w, "Channel is already in use", http.StatusConflict)
		return nil
	}
	// The upgrade process causes network I/O waits, so unlock it.
	clientConn.mu.Unlock()
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "Upgrade error", slog.String("error", err.Error()))
		return nil
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
		return nil
	}
	clientConn.wsConn = conn
	clientConn.attached.Store(true)
	clientConn.mu.Unlock()
	return conn
}

// serveWebSocket keeps the connection alive and passes every message from the client to onMessage
// until the client disconnects or the server shuts down.
func (h *base) serveWebSocket(channelID string, clientConn *ClientConn, conn *websocket.Conn, onMessage func(TunnelMessage)) { //nolint: gocognit
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
		_ = conn.Close()                                             //nolint: errcheck
		slog.Info(fmt.Sprintf("Client disconnected: %s", channelID)) //nolint:gosec
	}()

	// Goroutine that periodically sends pings
	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				clientConn.mu.Lock()
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
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
			onMessage(msg)
		}
	}()

	// Wait for a goroutine to terminate or for shutdown
	select {
	case <-done:
		// Normal disconnection
	case <-h.serverCtx.Done():
		// Server Shutdown
		slog.Info(fmt.Sprintf("Closing WebSocket connection due to server shutdown: %s", channelID)) //nolint:gosec
		clientConn.mu.Lock()
		_ = conn.WriteControl( //nolint: errcheck
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "Server is shutting down"),
			time.Now().Add(wsWriteTimeout),
		)
		clientConn.mu.Unlock()
		_ = conn.Close() //nolint: errcheck
	}
}

// pendingRequest is a webhook request waiting for the response from the client.
type pendingRequest struct {
	ch   chan TunnelMessage
	done chan struct{}
	once sync.Once
}

func newPendingRequest() *pendingRequest {
	return &pendingRequest{
		ch:   make(chan TunnelMessage, 32),
		done: make(chan struct{}),
	}
}

// deliver passes msg to the waiting handler. It returns false without blocking
// once the handler has returned (e.g. timed out), so a late response never stalls the caller.
func (p *pendingRequest) deliver(msg TunnelMessage) bool {
	select {
	case p.ch <- msg:
		return true
	case <-p.done:
		return false
	}
}

// finish tells the senders that nobody receives the response any more.
func (p *pendingRequest) finish() {
	p.once.Do(func() { close(p.done) })
}

// deliverResponse passes a response from the client to the handler waiting for the ReqID.
// It returns false when no handler on this node is waiting for it.
func (h *base) deliverResponse(msg TunnelMessage) bool {
	h.pendingMu.RLock()
	pending, exists := h.pendingRequests[msg.ReqID]
	h.pendingMu.RUnlock()
	if !exists {
		return false
	}
	return pending.deliver(msg)
}

// clientNotConnected answers a webhook whose channel nobody holds, before its body is read.
// Connection: close makes net/http skip draining the unread body (up to 256 KiB) before the
// response is sent, so the 404 is not delayed by a slow body and nothing more is read from it.
func clientNotConnected(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	http.Error(w, "Client not connected", http.StatusNotFound)
}

// webhookChannelID returns the channel ID of a /webhook/{channel_id}[/...] request, or "" when
// the path holds no valid channel ID. An ID that does not pass validChannelID can never have been
// issued, so the handlers answer 404 without looking the channel up.
func webhookChannelID(r *http.Request) string {
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/webhook/"), "/")[0]
	if !validChannelID(id) {
		return ""
	}
	return id
}

// proxyWebhook sends the webhook request with send and writes the response from the client.
// send returns the HTTP status to respond with when the request could not be sent.
func (h *base) proxyWebhook(w http.ResponseWriter, r *http.Request, send func(TunnelMessage) (int, error)) {
	// The webhook endpoint is unauthenticated, so bound the body read into memory (and relayed).
	r.Body = http.MaxBytesReader(w, r.Body, h.webhookMaxBodySize)
	// Convert HTTP requests directly into raw byte sequences (equivalent to TCP dumps)
	rawReqBytes, err := httputil.DumpRequest(r, true)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Error dumping request", http.StatusInternalServerError)
		return
	}

	reqID := uuid.New().String()
	pending := newPendingRequest()
	respCh := pending.ch

	h.pendingMu.Lock()
	h.pendingRequests[reqID] = pending
	h.pendingMu.Unlock()

	defer func() {
		pending.finish()
		h.pendingMu.Lock()
		delete(h.pendingRequests, reqID)
		h.pendingMu.Unlock()
	}()

	if status, err := send(TunnelMessage{ReqID: reqID, Payload: rawReqBytes}); err != nil {
		if status == http.StatusNotFound {
			// The endpoint is unauthenticated, so a webhook to an unknown channel is not worth a warning.
			slog.DebugContext(r.Context(), "webhook to a channel nobody holds", slog.String("error", err.Error()))
			// Same message as when the channel is not connected to this node.
			http.Error(w, "Client not connected", status)
			return
		}
		slog.WarnContext(r.Context(), "failed to send webhook to client", slog.String("error", err.Error()))
		http.Error(w, http.StatusText(status), status)
		return
	}

	// Waiting for a response from the client
	timer := time.NewTimer(webhookResponseTimeout)
	defer timer.Stop()
	select {
	case firstMsg := <-respCh:
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(firstMsg.Payload)), r)
		if err != nil {
			http.Error(w, "Bad gateway response from client", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close() //nolint: errcheck
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		drainResponseChunks(w, respCh, timer)
	case <-timer.C:
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
	}
}

func drainResponseChunks(w http.ResponseWriter, respCh <-chan TunnelMessage, timer *time.Timer) {
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}
	for {
		select {
		case chunkMsg := <-respCh:
			if chunkMsg.EOF {
				return
			}
			if _, err := w.Write(chunkMsg.Payload); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		case <-timer.C:
			return
		}
	}
}

func (h *base) handleWaitHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Create a channel to receive tokens and register it in the map.
	// It is buffered so that the sender never blocks even if this handler has already returned.
	tokenChan := make(chan string, 1)
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

func (h *base) handleAuthGitHub(w http.ResponseWriter, r *http.Request) {
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

func (h *base) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
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
	if err := h.cluster.Broadcast(r.Context(), msg); err != nil {
		slog.ErrorContext(r.Context(), "failed to broadcast auth message", slog.String("error", err.Error()))
	}
	h.sendToken(sessionID, sessionToken)
	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`Login complete. <br />Please close your browser.`)) //nolint: errcheck,gosec
	slog.InfoContext(r.Context(), "user authenticated", slog.String("username", username))
}

func (h *base) CleanNonActiveSession() {
	h.activeChannelsMu.RLock()
	nonActiveSession := make([]string, 0, len(h.activeChannels))
	for id, client := range h.activeChannels {
		if !client.connected() {
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

// sendToken passes the token to the CLI waiting on this node for the session, if any.
// It never blocks: the CLI may have disconnected, or the token may have already been delivered.
func (h *base) sendToken(sessionID, token string) {
	ch, ok := h.waitingClients.Load(sessionID)
	if !ok {
		return
	}
	c, ok := ch.(chan string)
	if !ok {
		return
	}
	select {
	case c <- token:
	default:
	}
}

func (h *base) notifyMsg(notifyMsg *cluster.NotifyMsg) {
	switch notifyMsg.MsgType {
	case cluster.NotifyMsgType_AuthMessage:
		msg, err := cluster.DecodeNotifyMsg[cluster.AuthMessage](notifyMsg)
		if err != nil {
			slog.Error(err.Error())
			return
		}
		h.sendToken(msg.SessionID, msg.Token)
	}
}

const localhost = "127.0.0.1"

func GetLocalIP() string {
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
