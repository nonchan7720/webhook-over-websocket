package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
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

	insecure bool
}

func clientCommand() *cobra.Command {
	var args clientArgs
	cmd := &cobra.Command{
		Use:           "client",
		SilenceErrors: true,
		SilenceUsage:  true,
		PreRun: func(cmd *cobra.Command, _ []string) {
			if args.insecure {
				tlsConfig := &tls.Config{
					InsecureSkipVerify: true, //nolint: gosec
				}
				transport := http.DefaultTransport.(*http.Transport).Clone() // nolint: errcheck,forcetypeassert
				transport.TLSClientConfig = tlsConfig
				http.DefaultClient.Transport = transport
				websocket.DefaultDialer.TLSClientConfig = tlsConfig
			}
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeClient(cmd.Context(), &args)
		},
	}
	flag := cmd.Flags()
	flag.StringVar(&args.serverURL, "server-url", "", "webhook-over-websocket server URL (e.g. http://example.com)")
	flag.StringVar(&args.targetURL, "target-url", "http://localhost:3000", "local server URL to forward webhook requests to")
	flag.BoolVar(&args.insecure, "insecure", false, "insecure skip verify")
	_ = cmd.MarkFlagRequired("server-url") //nolint: errcheck
	return cmd
}

func executeClient(ctx context.Context, args *clientArgs) error { //nolint: gocognit,cyclop
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

	fmt.Printf("Issued Channel ID: %s\n", channelID)
	fmt.Printf("Please set the webhook destination as follows: %s/webhook/%s\n", args.serverURL, channelID)

	// 2. サーバーにWebSocketで接続
	dialer := websocket.DefaultDialer
	if args.insecure {
		tls := &tls.Config{
			InsecureSkipVerify: true,                 //nolint: gosec
			NextProtos:         []string{"http/1.1"}, // h2 を含めない
		}
		dialer.TLSClientConfig = tls
	}
	wsURL := fmt.Sprintf("%s://%s/ws/%s", websocketScheme, u.Host, channelID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	defer conn.Close() //nolint: errcheck
	slog.Info("A tunnel to the server has been established.")

	var wsMutex sync.Mutex

	// Set the handler for Ping/Pong processing
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // nolint: errcheck
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		wsMutex.Lock()
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		wsMutex.Unlock()
		if err != nil {
			return err
		}
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second)) // nolint: errcheck

	done := make(chan struct{})

	// Properly close WebSockets when canceling the context
	go func() {
		<-ctx.Done()
		slog.Info("Shutting down client...")
		wsMutex.Lock()
		_ = conn.WriteMessage( //nolint: errcheck
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "Client is shutting down"),
		)
		wsMutex.Unlock()
		_ = conn.Close() //nolint: errcheck
	}()

	// A goroutine that periodically sends pings
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				wsMutex.Lock()
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					wsMutex.Unlock()
					return
				}
				wsMutex.Unlock()
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Execute the message reception loop in a goroutine
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			var msg TunnelMessage
			err := conn.ReadJSON(&msg)
			if err != nil {
				select {
				case <-ctx.Done():
					// When canceled, it exits normally.
					errCh <- ctx.Err()
				default:
					slog.Error(fmt.Sprintf("WebSocket Disconnection: %v", err))
					errCh <- err
				}
				return
			}

			// Forward each request to the local server in parallel processing
			go handleHTTPRequest(ctx, msg, conn, &wsMutex, args.targetURL)
		}
	}()

	// Waiting for completion
	select {
	case err := <-errCh:
		if err == context.Canceled || err == context.DeadlineExceeded {
			slog.Info("Client shutdown completed")
			return nil
		}
		return err
	case <-ctx.Done():
		slog.Info("Context cancelled, waiting for cleanup...")
		// Wait for termination from errCh
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			slog.Warn("Cleanup timeout")
		}
		return nil
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
	query := req.URL.Query()
	for key, val := range target.Query() {
		for _, v := range val {
			query.Add(key, v)
		}
	}
	target.RawQuery = query.Encode()
	req.URL = target
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
