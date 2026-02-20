package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

type clientArgs struct {
	serverURL string
	targetURL string
}

func clientCommand() *cobra.Command {
	var args clientArgs
	cmd := &cobra.Command{
		Use:           "client",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeClient(cmd.Context(), &args)
		},
	}
	flag := cmd.Flags()
	flag.StringVar(&args.serverURL, "server-url", "", "webhook-over-websocket server URL (e.g. http://example.com)")
	flag.StringVar(&args.targetURL, "target-url", "http://localhost:3000", "local server URL to forward webhook requests to")
	_ = cmd.MarkFlagRequired("server-url") //nolint: errcheck
	return cmd
}

func executeClient(ctx context.Context, args *clientArgs) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	u, err := url.Parse(args.serverURL)
	if err != nil {
		return fmt.Errorf("Failed to parse server url: %w", err) //nolint:staticcheck
	}
	isTLSConn := u.Scheme == "https"
	websocketScheme := "ws"
	if isTLSConn {
		websocketScheme = "wss"
	}
	// 1. サーバーから channel_id を発番してもらう
	channelID, err := getNewChannel(args.serverURL)
	if err != nil {
		return fmt.Errorf("failed to retrieve channel_id: %w", err)
	}

	fmt.Printf("Issued Channel ID: %s", channelID)
	fmt.Printf("Please set the webhook destination as follows: %s/webhook/%s", args.serverURL, channelID)

	// 2. サーバーにWebSocketで接続
	wsURL := fmt.Sprintf("%s://%s/ws/%s", websocketScheme, u.Host, channelID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	defer conn.Close() //nolint: errcheck
	slog.Info("A tunnel to the server has been established.")

	var wsMutex sync.Mutex

	// contextキャンセル時にWebSocketを閉じる
	go func() {
		<-ctx.Done()
		slog.Info("Shutting down client...")
		_ = conn.Close() //nolint: errcheck
	}()

	// 3. メッセージ受信ループ
	for {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, exiting...")
			return ctx.Err()
		default:
		}

		var msg TunnelMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Info("Context cancelled during read")
				return ctx.Err()
			default:
				slog.Error(fmt.Sprintf("WebSocket Disconnection: %v", err))
				return err
			}
		}

		// リクエストごとに並行処理でローカルへフォワード
		go handleHTTPRequest(ctx, msg, conn, &wsMutex, args.targetURL)
	}
}

// getNewChannel はサーバーの /new を叩いて channel_id を取得します
func getNewChannel(serverURL string) (string, error) {
	resp, err := http.Get(serverURL + "/new")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint: errcheck

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["channel_id"], nil
}

// handleHTTPRequest は受信したバイト列を復元し、ローカルへ送信、結果を返却します
func handleHTTPRequest(ctx context.Context, msg TunnelMessage, wsConn *websocket.Conn, wsMutex *sync.Mutex, targetURL string) {
	slog.Info(fmt.Sprintf("[ReqID: %s] Webhookを受信、ローカルへ転送します...", msg.ReqID))

	// 1. 生のバイト列を http.Request に復元
	reqReader := bufio.NewReader(bytes.NewReader(msg.Payload))
	req, err := http.ReadRequest(reqReader)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] リクエスト復元エラー: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}

	// 2. ローカルサーバー向けにリクエスト情報を書き換え
	req.RequestURI = "" // クライアントとして送信する場合は空にする必要がある
	target, err := url.Parse(targetURL)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] ターゲットURL解析エラー: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host
	req = req.WithContext(ctx)

	// 3. ローカルサーバーへ送信
	// ※ タイムアウトを設定した専用のhttp.Clientを使うのが実用的です
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] ローカルサーバーへの送信エラー: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}
	defer resp.Body.Close() //nolint: errcheck

	// 4. 受け取ったレスポンスを生のバイト列にダンプ
	rawRespBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] レスポンスダンプエラー: %v", msg.ReqID, err))
		return
	}

	// 5. サーバーへ結果を返送
	respMsg := TunnelMessage{
		ReqID:   msg.ReqID,
		Payload: rawRespBytes,
	}

	wsMutex.Lock()
	_ = wsConn.WriteJSON(respMsg) //nolint: errcheck
	wsMutex.Unlock()

	slog.Info(fmt.Sprintf("[ReqID: %s] ローカルのレスポンスをサーバーへ返却しました (Status: %d)", msg.ReqID, resp.StatusCode))
}

// sendErrorResponse はローカルに繋がらない時などに 502 Bad Gateway を返す
func sendErrorResponse(reqID string, wsConn *websocket.Conn, wsMutex *sync.Mutex) {
	badGatewayResp := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	msg := TunnelMessage{
		ReqID:   reqID,
		Payload: []byte(badGatewayResp),
	}
	wsMutex.Lock()
	_ = wsConn.WriteJSON(msg) //nolint: errcheck
	wsMutex.Unlock()
}
