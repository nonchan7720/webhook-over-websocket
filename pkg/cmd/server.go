package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	"github.com/spf13/cobra"
)

type serverArgs struct {
	port       int
	peerDomain string
}

func serverCommand() *cobra.Command {
	var args serverArgs
	cmd := &cobra.Command{
		Use: "server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeServer(cmd.Context(), &args)
		},
	}
	flag := cmd.Flags()
	flag.IntVarP(&args.port, "port", "p", 8080, "server port")
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
	// 1. クライアントが起動時に channel_id を発番するためのエンドポイント
	mux.HandleFunc("/new", handler.handleNewChannel)
	// 2. Traefik の HTTP Provider が定期的に見に来る設定出力エンドポイント
	mux.HandleFunc("/traefik-config", handler.handleTraefikConfig)
	// 3. ピア同士が情報を共有するための内部エンドポイント(追加)
	mux.HandleFunc("/internal/channels", handler.handleInternalChannels)
	// 4. クライアントからのWebSocket接続待ち受け
	mux.HandleFunc("/ws/", handler.handleWebSocket)
	// 5. Traefik経由で送られてくる外部Webhookの受け口
	mux.HandleFunc("/webhook/", handler.handleWebhook)
	srv := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 20 * time.Second,
	}
	log.Println("Server listening on :8080")
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("failed to run server", slog.String("error", err.Error()))
		}
	}()

	<-ctx.Done()
	tCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	slog.InfoContext(tCtx, "Stop server")
	defer cancel()
	return srv.Shutdown(tCtx)
}

// TunnelMessage はサーバー・クライアント間でやり取りするメッセージ構造体
type TunnelMessage struct {
	ReqID   string `json:"req_id"`
	Payload []byte `json:"payload"`
}

// 接続中のクライアント管理
type ClientConn struct {
	wsConn *websocket.Conn
	mu     sync.Mutex // WebSocketの同時書き込みを防ぐため
}

var (
	// channel_id -> クライアントのWebSocketコネクション
	activeChannels = make(map[string]*ClientConn)
	channelsMu     sync.RWMutex

	// req_id -> レスポンス待ち受け用チャネル
	pendingRequests = make(map[string]chan []byte)
	pendingMu       sync.RWMutex

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// 環境変数からの設定（スケールアウト用）
	myIP = getLocalIP()
)

// /new: 新しい channel_id (UUID) を生成して返す
func (h *serverHandle) handleNewChannel(w http.ResponseWriter, r *http.Request) {
	channelID := uuid.New().String()

	// ※ 本来はここでチャネルの有効期限などをDBに記録しますが、
	// 今回は簡単のため、WebSocketが繋がった時点で activeChannels に登録します。

	resp := map[string]string{"channel_id": channelID}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp) //nolint: errcheck,errchkjson
	log.Printf("新しい Channel ID を発番しました: %s", channelID)
}

// ピア通信用の構造体
type InternalChannelsResp struct {
	Channels  []string `json:"channels"`
	ServerURL string   `json:"server_url"`
}

type serverHandle struct {
	myServerURL string
	peerDomain  string
	port        int
}

// /internal/channels: 自分が保持しているチャネル(UUID)一覧を返す
func (h *serverHandle) handleInternalChannels(w http.ResponseWriter, r *http.Request) {
	var channels []string
	channelsMu.RLock()
	for id := range activeChannels {
		channels = append(channels, id)
	}
	channelsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&InternalChannelsResp{ //nolint: errcheck,errchkjson
		Channels:  channels,
		ServerURL: h.myServerURL,
	})
}

// /traefik-config: Traefikが動的ルーティングを作るためのJSONを返す
func (h *serverHandle) handleTraefikConfig(w http.ResponseWriter, r *http.Request) {
	type Server struct {
		URL string `json:"url"`
	}
	type LoadBalancer struct {
		Servers []Server `json:"servers"`
	}
	type Service struct {
		LoadBalancer LoadBalancer `json:"loadBalancer"`
	}
	type Router struct {
		Rule    string `json:"rule"`
		Service string `json:"service"`
	}
	type HTTPConfig struct {
		Routers  map[string]Router  `json:"routers"`
		Services map[string]Service `json:"services"`
	}
	type TraefikProvider struct {
		HTTP HTTPConfig `json:"http"`
	}

	config := TraefikProvider{
		HTTP: HTTPConfig{
			Routers:  make(map[string]Router),
			Services: make(map[string]Service),
		},
	}

	allChannels := make(map[string]InternalChannelsResp) // key: ServerURL

	// 1. まず自分自身の情報を取得
	myChannels := []string{}
	channelsMu.RLock()
	for id := range activeChannels {
		myChannels = append(myChannels, id)
	}
	channelsMu.RUnlock()

	allChannels[h.myServerURL] = InternalChannelsResp{
		Channels:  myChannels,
		ServerURL: h.myServerURL,
	}

	// 2. ピア(兄弟ノード)の情報を取りに行く
	if peerDomain := h.peerDomain; peerDomain != "" { //nolint: nestif
		ips, err := net.LookupIP(peerDomain)
		if err != nil {
			log.Printf("DNS lookup failed for %s: %v", peerDomain, err)
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
		for _, channelID := range info.Channels {
			routerName := "router-" + channelID
			serviceName := "service-" + channelID

			config.HTTP.Routers[routerName] = Router{
				// 例: /webhook/1234-abcd へのアクセスをこのサービスに流す
				Rule:    fmt.Sprintf("PathPrefix(`/webhook/%s`)", channelID),
				Service: serviceName,
			}
			config.HTTP.Services[serviceName] = Service{
				LoadBalancer: LoadBalancer{
					// その UUID を持っている固有のサーバーへ転送する
					Servers: []Server{{URL: serverURL}},
				},
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(config) //nolint: errcheck,errchkjson
}

// 他ノードからチャネル一覧を取得するヘルパー関数
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

// /ws/{channel_id}: クライアントからのWebSocket接続
func (h *serverHandle) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	channelID := strings.TrimPrefix(r.URL.Path, "/ws/")
	if channelID == "" {
		http.Error(w, "Missing channel_id", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	clientConn := &ClientConn{wsConn: conn}

	channelsMu.Lock()
	activeChannels[channelID] = clientConn
	channelsMu.Unlock()

	log.Printf("Client connected: %s", channelID)

	defer func() {
		channelsMu.Lock()
		delete(activeChannels, channelID)
		channelsMu.Unlock()
		_ = conn.Close() //nolint: errcheck
		log.Printf("Client disconnected: %s", channelID)
	}()

	// WebSocketからクライアントのレスポンスを受信するループ
	for {
		var msg TunnelMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			break // 切断
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

// /webhook/{channel_id}: 外部からTraefik経由で届くWebhookを受け取る
func (h *serverHandle) handleWebhook(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/webhook/"), "/")
	channelID := parts[0]

	channelsMu.RLock()
	client, exists := activeChannels[channelID]
	channelsMu.RUnlock()

	if !exists {
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
