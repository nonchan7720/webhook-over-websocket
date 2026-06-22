package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/nonchan7720/webhook-over-websocket/pkg/retry"
	"github.com/nonchan7720/webhook-over-websocket/pkg/utils"
	"github.com/spf13/cobra"
)

type clientArgs struct {
	serverURL string
	targetURL string
	channelID string

	insecure bool

	transferRequestTimeout        time.Duration
	disableTransferRequestTimeout bool
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
	flag.StringVar(&args.channelID, "channel-id", "", "channel ID to use")
	flag.BoolVar(&args.insecure, "insecure", false, "insecure skip verify")
	flag.DurationVar(
		&args.transferRequestTimeout,
		"transfer-request-timeout",
		10*time.Second,
		"Timeout for transfers to the local server",
	)
	flag.BoolVar(
		&args.disableTransferRequestTimeout,
		"disabled-transfer-request-timeout",
		false,
		"Disable the timeout when transfers to the local server",
	)
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

	token, err := authorization(ctx, args.serverURL)
	if err != nil {
		return fmt.Errorf("failed to authorization step: %w", err)
	}

	// Have the server generate or use the provided channel_id
	channelID, err := getNewChannel(args.serverURL, token, args.channelID)
	if err != nil {
		return fmt.Errorf("failed to retrieve channel_id: %w", err)
	}

	fmt.Printf("Issued Channel ID: %s\n", channelID)
	fmt.Printf("Please set the webhook destination as follows: %s/webhook/%s\n", args.serverURL, channelID)

	// Connect to the server via WebSocket
	dialer := websocket.DefaultDialer
	if args.insecure {
		tls := &tls.Config{
			InsecureSkipVerify: true,                 //nolint: gosec
			NextProtos:         []string{"http/1.1"}, // Do not include h2
		}
		dialer.TLSClientConfig = tls
	}
	wsURL := fmt.Sprintf("%s://%s/ws/%s", websocketScheme, u.Host, channelID)

	// Build WebSocket headers (include Authorization if a channel token was issued)
	var wsHeaders http.Header
	if token != "" {
		wsHeaders = http.Header{"Authorization": []string{"Bearer " + token}}
	}

	conn, err := retry.Retry(ctx, func() (*websocket.Conn, error) {
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, wsHeaders)
		if err != nil {
			return nil, fmt.Errorf("WebSocket connection failed: %w", err)
		}
		return conn, nil
	})
	if err != nil {
		return err
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
			go handleHTTPRequest(
				ctx,
				msg,
				conn,
				&wsMutex,
				args.targetURL,
				channelID,
				args.transferRequestTimeout,
				args.disableTransferRequestTimeout,
			)
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

// getNewChannel hits the server's /new endpoint to retrieve the channel_id and optional channel token.
func getNewChannel(serverURL, token, channelID string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Path = "/new"
	if channelID != "" {
		q := u.Query()
		q.Set("channel_id", channelID)
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint: errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/new returned status %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["channel_id"], nil
}

// handleHTTPRequest reconstructs the received byte stream, sends it locally, and returns the result.
func handleHTTPRequest(
	ctx context.Context,
	msg TunnelMessage,
	wsConn *websocket.Conn,
	wsMutex *sync.Mutex,
	targetURL string,
	channelID string,
	timeout time.Duration,
	disabledTimeout bool,
) {
	slog.Info(fmt.Sprintf("[ReqID: %s] Receive webhooks and forward them locally....", msg.ReqID))

	// Restore the raw byte array to an HTTP request
	reqReader := bufio.NewReader(bytes.NewReader(msg.Payload))
	req, err := http.ReadRequest(reqReader)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Request Restore Error: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}

	// Wire req.Body/GetBody to the body slice of msg.Payload without copying,
	// enabling Go's http.Client to replay the body on 307/308 redirects.
	wireRequestBody(req, msg.Payload)

	// Rewrite request information for the local server
	req.RequestURI = "" // NOTE: When sending as a client, it must be left blank.
	target, err := url.Parse(targetURL)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Target URL Parsing Error: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}

	// Strip /webhook/{channelID} prefix from the request path, then merge with the
	// target's base path so that dynamic paths are forwarded to the local server.
	// e.g. /webhook/uuid/api/users + target=http://localhost:3000/base → http://localhost:3000/base/api/users
	rawPath := req.URL.Path
	pathPrefix := "/webhook/" + channelID
	pathSuffix := strings.TrimPrefix(rawPath, pathPrefix)
	if pathSuffix == "" || pathSuffix[0] != '/' {
		pathSuffix = "/" + pathSuffix
	}
	originalPath := pathSuffix
	query := req.URL.Query()
	for key, val := range target.Query() {
		for _, v := range val {
			query.Add(key, v)
		}
	}
	mergedPath := path.Join(target.Path, originalPath)
	if strings.HasSuffix(originalPath, "/") && !strings.HasSuffix(mergedPath, "/") {
		mergedPath += "/"
	}
	target.Path = mergedPath
	target.RawQuery = query.Encode()
	req.URL = target
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host
	req = req.WithContext(ctx)

	// Send to local server
	client := &http.Client{}
	if !disabledTimeout && timeout > 0 {
		client.Timeout = timeout
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Error sending to local server: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}
	defer resp.Body.Close() //nolint: errcheck

	if err := streamResponse(msg.ReqID, resp, wsConn, wsMutex); err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Response Stream Error: %v", msg.ReqID, err))
		return
	}
	slog.Info(fmt.Sprintf("[ReqID: %s] The local response has been returned to the server. (Status: %d)", msg.ReqID, resp.StatusCode))
}

// wireRequestBody points req.Body and GetBody directly at the body slice within
// raw (the original WebSocket payload) without copying, so Go's http.Client can
// replay the body on 307/308 redirects.
func wireRequestBody(req *http.Request, raw []byte) {
	sep := bytes.Index(raw, []byte("\r\n\r\n"))
	if sep < 0 {
		return
	}
	body := raw[sep+4:]
	if len(body) == 0 {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
}

const streamChunkSize = 32 * 1024

func streamResponse(reqID string, resp *http.Response, wsConn *websocket.Conn, wsMutex *sync.Mutex) error {
	var hdr bytes.Buffer
	fmt.Fprintf(&hdr, "HTTP/%d.%d %d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode, http.StatusText(resp.StatusCode)) //nolint:errcheck
	if err := resp.Header.Write(&hdr); err != nil {
		return err
	}
	hdr.WriteString("\r\n")

	wsMutex.Lock()
	err := wsConn.WriteJSON(TunnelMessage{ReqID: reqID, Payload: hdr.Bytes()})
	wsMutex.Unlock()
	if err != nil {
		return err
	}

	buf := make([]byte, streamChunkSize)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			wsMutex.Lock()
			err = wsConn.WriteJSON(TunnelMessage{ReqID: reqID, Payload: chunk})
			wsMutex.Unlock()
			if err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	wsMutex.Lock()
	err = wsConn.WriteJSON(TunnelMessage{ReqID: reqID, EOF: true})
	wsMutex.Unlock()
	return err
}

func sendErrorResponse(reqID string, wsConn *websocket.Conn, wsMutex *sync.Mutex) {
	header := []byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	wsMutex.Lock()
	_ = wsConn.WriteJSON(TunnelMessage{ReqID: reqID, Payload: header}) //nolint: errcheck
	_ = wsConn.WriteJSON(TunnelMessage{ReqID: reqID, EOF: true})        //nolint: errcheck
	wsMutex.Unlock()
}

func authorization(ctx context.Context, serverURL string) (string, error) {
	sessionID := fmt.Sprintf("sess_%s", uuid.NewString())
	tokenChan := make(chan string, 1)
	isAuthCh := make(chan bool, 1)
	sessionWaitURL := fmt.Sprintf("%s/auth/client?session_id=%s", serverURL, sessionID)
	req, err := http.NewRequest(http.MethodGet, sessionWaitURL, nil)
	if err != nil {
		return "", err
	}
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("サーバーへの接続に失敗しました: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close() //nolint: errcheck

		if resp.StatusCode == http.StatusNotFound {
			isAuthCh <- false
			return
		}
		isAuthCh <- true
		// Read the SSE stream
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			// "data: <token>" format
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				token := after
				tokenChan <- token
				return // When the token is received, terminate the goroutine.
			}
		}
	}()

	// Wait a moment for the server-side connection to be established (to ensure reliable processing).
	time.Sleep(500 * time.Millisecond)
	isAuth := <-isAuthCh
	if isAuth {
		loginURL := fmt.Sprintf("%s/auth/login?session_id=%s", serverURL, sessionID)
		if err := utils.OpenBrowser(loginURL); err != nil {
			fmt.Printf("ブラウザを開いてログインを完了させてください...: %s\n", loginURL)
		}
		select {
		case token := <-tokenChan:
			return token, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	} else {
		return "", nil
	}
}
