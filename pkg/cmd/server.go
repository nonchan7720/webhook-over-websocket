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
	"github.com/nonchan7720/webhook-over-websocket/pkg/traefik"
	"github.com/spf13/cobra"
)

var (
	activeChannels   map[string]*ClientConn
	activeChannelsMu sync.RWMutex

	pendingRequests map[string]chan []byte
	pendingMu       sync.RWMutex

	upgrader websocket.Upgrader

	myIP string
)

type serverArgs struct {
	port       int
	peerDomain string

	cleanupDuration time.Duration
}

func serverCommand() *cobra.Command {
	var args serverArgs
	cmd := &cobra.Command{
		Use: "server",
		PreRun: func(cmd *cobra.Command, args []string) {
			myIP = getLocalIP()
			activeChannels = make(map[string]*ClientConn)
			pendingRequests = make(map[string]chan []byte)
			upgrader = websocket.Upgrader{
				CheckOrigin: func(r *http.Request) bool { return true },
			}
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeServer(cmd.Context(), &args)
		},
	}
	flag := cmd.Flags()
	flag.IntVarP(&args.port, "port", "p", 8080, "server port")
	flag.StringVar(&args.peerDomain, "peer-domain", "", "peer domain name")
	flag.DurationVar(&args.cleanupDuration, "cleanup-duration", 5*time.Minute, "channel_id cleanup duration")
	return cmd
}

func executeServer(ctx context.Context, args *serverArgs) error {
	handler := &serverHandle{
		peerDomain:  args.peerDomain,
		myServerURL: fmt.Sprintf("http://%s:%d", myIP, args.port),
		port:        args.port,
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.port))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	// Endpoint for clients to generate channelId upon startup
	mux.HandleFunc("/new", handler.handleNewChannel)
	// The HTTP Provider in Traefik periodically checks the configuration output endpoint.
	mux.HandleFunc("/traefik-config", handler.handleTraefikConfig)
	// Internal endpoint for peers to share information (additional)
	mux.HandleFunc("/internal/channels", handler.handleInternalChannels)
	// Waiting for WebSocket connections from clients
	mux.HandleFunc("/ws/{channelId}", handler.handleWebSocket)
	// External webhook reception point via Traefik
	mux.HandleFunc("/webhook/", handler.handleWebhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"OK"}`)) //nolint:errcheck
	})
	srv := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 20 * time.Second,
	}
	slog.Info(fmt.Sprintf("Server listening on :%d", args.port))
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("failed to run server", slog.String("error", err.Error()))
		}
	}()
	go func() {
		ticker := time.NewTicker(args.cleanupDuration)
		select {
		case <-ticker.C:
			cleanNonActiveSession()
		case <-ctx.Done():
			return
		}
	}()

	<-ctx.Done()
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
	mu     sync.Mutex // WebSocketの同時書き込みを防ぐため
}

func (c *ClientConn) isActive() bool {
	return c.wsConn != nil
}

func (h *serverHandle) handleNewChannel(w http.ResponseWriter, r *http.Request) {
	channelID := uuid.New().String()
	clientConn := &ClientConn{wsConn: nil}
	activeChannelsMu.Lock()
	activeChannels[channelID] = clientConn
	activeChannelsMu.Unlock()
	resp := map[string]string{"channel_id": channelID}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp) //nolint: errcheck,errchkjson
	slog.Info("new Channel ID has been issued", slog.String("channel-id", channelID))
}

type InternalChannelsResp struct {
	WsChannels      []string `json:"ws_channels"`
	WebhookChannels []string `json:"webhook_channels"`
	ServerURL       string   `json:"server_url"`
}

type serverHandle struct {
	myServerURL string
	peerDomain  string
	port        int
}

func (h *serverHandle) handleInternalChannels(w http.ResponseWriter, r *http.Request) {
	var wsChannels []string
	var webhookChannels []string

	activeChannelsMu.RLock()
	for id, client := range activeChannels {
		// WS用のルーターは未接続（発行済み）でも作成する
		wsChannels = append(wsChannels, id)
		// Webhook用のルーターは実際に接続済み（isActive）の時のみ作成する
		if client.isActive() {
			webhookChannels = append(webhookChannels, id)
		}
	}
	activeChannelsMu.RUnlock()

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

	// 1. まず自分自身の情報を取得
	var myWsChannels []string
	var myWebhookChannels []string

	activeChannelsMu.RLock()
	for id, client := range activeChannels {
		myWsChannels = append(myWsChannels, id) // WS用は全て追加
		if client.isActive() {
			myWebhookChannels = append(myWebhookChannels, id) // Webhook用は接続中のみ追加
		}
	}
	activeChannelsMu.RUnlock()

	allChannels[h.myServerURL] = InternalChannelsResp{
		WsChannels:      myWsChannels,
		WebhookChannels: myWebhookChannels,
		ServerURL:       h.myServerURL,
	}

	// 2. ピア(兄弟ノード)の情報を取りに行く
	if peerDomain := h.peerDomain; peerDomain != "" { //nolint: nestif
		ips, err := net.LookupIP(peerDomain)
		if err != nil {
			slog.Info(fmt.Sprintf("DNS lookup failed for %s: %v", peerDomain, err))
		} else {
			var wg sync.WaitGroup
			infoCh := make(chan InternalChannelsResp, len(ips))

			for _, ip := range ips {
				wg.Add(1)
				go fetchPeerChannels(net.JoinHostPort(ip.String(), fmt.Sprintf("%d", h.port)), infoCh, &wg)
			}

			wg.Wait()
			close(infoCh)

			for info := range infoCh {
				// 自分の情報はメモリのものが最新なので上書きしない
				if info.ServerURL != h.myServerURL {
					allChannels[info.ServerURL] = info
				}
			}
		}
	}

	// 3. 全ノードの情報をマージしてTraefik用のJSONを作る
	for serverURL, info := range allChannels {
		// まず、必要なサービス定義を一意にして作成する
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
		// Webhook接続用のルーター（接続中のクライアントのみ）
		for _, channelID := range info.WebhookChannels {
			webhookRouterName := "webhook-" + channelID
			serviceName := "service-" + channelID

			config.HTTP.Routers[webhookRouterName] = traefik.RouterConfig{
				Rule:    fmt.Sprintf("PathPrefix(`/webhook/%s`)", channelID),
				Service: serviceName,
			}
		}
		// WebSocket接続用のルーター（未接続含む全チャネル）
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
	client := http.Client{Timeout: 2 * time.Second} // 応答を待たせないように短めに
	url := fmt.Sprintf("http://%s/internal/channels", hostPort)
	resp, err := client.Get(url)
	if err != nil {
		// ゴーストコンテナ等へは疎通できないため無視する
		return
	}
	defer resp.Body.Close() //nolint: errcheck

	var info InternalChannelsResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err == nil {
		ch <- info
	}
}

func (h *serverHandle) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelId")
	if channelID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}
	activeChannelsMu.RLock()
	clientConn, exists := activeChannels[channelID]
	activeChannelsMu.RUnlock()
	if !exists {
		http.Error(w, "Forbidden or invalid channel_id", http.StatusForbidden)
		return
	}

	clientConn.mu.Lock()
	if clientConn.isActive() {
		clientConn.mu.Unlock()
		http.Error(w, "Channel is already in use", http.StatusConflict)
		return
	}
	// Upgrade処理でネットワークI/O待ちが発生するため、ロックを外す
	clientConn.mu.Unlock()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "Upgrade error", slog.String("error", err.Error()))
		return
	}
	// Upgrade成功後、再度ロックを取って格納（間に横取りされていないか最終確認）
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

	slog.Info(fmt.Sprintf("Client connected: %s", channelID))

	defer func() {
		activeChannelsMu.Lock()
		delete(activeChannels, channelID)
		activeChannelsMu.Unlock()
		_ = conn.Close() //nolint: errcheck
		slog.Info(fmt.Sprintf("Client disconnected: %s", channelID))
	}()

	// WebSocketからクライアントのレスポンスを受信するループ
	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			break // 切断
		}

		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue // 制御フレームなどはスキップ
		}

		var msg TunnelMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			slog.Warn("Failed to unmarshal tunnel message", slog.String("error", err.Error()))
			continue
		}

		// 該当するReqIDで待機しているハンドラにレスポンスを渡す
		pendingMu.RLock()
		respCh, exists := pendingRequests[msg.ReqID]
		pendingMu.RUnlock()

		if exists {
			respCh <- msg.Payload
		}
	}
}

func (h *serverHandle) handleWebhook(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/webhook/"), "/")
	channelID := parts[0]

	activeChannelsMu.RLock()
	client, exists := activeChannels[channelID]
	activeChannelsMu.RUnlock()

	if !exists || client.wsConn == nil {
		http.Error(w, "Client not connected", http.StatusNotFound)
		return
	}

	// 1. HTTPリクエストをそのまま生のバイト列(TCPダンプ相当)に変換
	rawReqBytes, err := httputil.DumpRequest(r, true)
	if err != nil {
		http.Error(w, "Error dumping request", http.StatusInternalServerError)
		return
	}

	reqID := uuid.New().String()
	respCh := make(chan []byte)

	pendingMu.Lock()
	pendingRequests[reqID] = respCh
	pendingMu.Unlock()

	defer func() {
		pendingMu.Lock()
		delete(pendingRequests, reqID)
		pendingMu.Unlock()
	}()

	// 2. WebSocketへ送信
	msg := TunnelMessage{ReqID: reqID, Payload: rawReqBytes}
	client.mu.Lock()
	err = client.wsConn.WriteJSON(msg)
	client.mu.Unlock()

	if err != nil {
		http.Error(w, "Failed to send to client", http.StatusBadGateway)
		return
	}

	// 3. クライアントからのレスポンスを待つ
	select {
	case rawRespBytes := <-respCh:
		// 生のバイト列を http.Response オブジェクトに復元
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(rawRespBytes)), r)
		if err != nil {
			http.Error(w, "Bad gateway response from client", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close() //nolint: errcheck,errchkjson

		// ヘッダーをコピー
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
	// K8s環境: 環境変数からPod IPを取得
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
			slog.Info(fmt.Sprintf("Using POD_IP from environment: %s", podIP))
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
		// スキップ: ダウンしているインターフェース、ループバック
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

				// 優先インターフェース名にマッチする場合は即座に返す
				for _, prefix := range preferredNames {
					if len(iface.Name) >= len(prefix) && iface.Name[:len(prefix)] == prefix {
						slog.Info(fmt.Sprintf("Using IP from %s: %s", iface.Name, ip))
						return ip
					}
				}

				// 候補として保持
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

func cleanNonActiveSession() {
	activeChannelsMu.RLock() // 【修正】並行アクセス(panic)を防ぐため RLock を追加
	nonActiveSession := make([]string, 0, len(activeChannels))
	for id, client := range activeChannels {
		if !client.isActive() {
			nonActiveSession = append(nonActiveSession, id)
		}
	}
	activeChannelsMu.RUnlock() // 読み取り完了後にロック解除
	if len(nonActiveSession) == 0 {
		return
	}
	activeChannelsMu.Lock()
	defer activeChannelsMu.Unlock()
	for _, id := range nonActiveSession {
		delete(activeChannels, id)
	}
}
