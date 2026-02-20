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
	// Have the server generate a channel_id
	channelID, err := getNewChannel(args.serverURL)
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
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	defer conn.Close() //nolint: errcheck
	slog.Info("A tunnel to the server has been established.")

	var wsMutex sync.Mutex

	// Close the WebSocket when canceling the context
	go func() {
		<-ctx.Done()
		slog.Info("Shutting down client...")
		_ = conn.Close() //nolint: errcheck
	}()

	// Message Receive Loop
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

		// Forward each request to the local server in parallel processing
		go handleHTTPRequest(
			ctx,
			msg,
			conn,
			&wsMutex,
			args.targetURL,
			args.transferRequestTimeout,
			args.disableTransferRequestTimeout,
		)
	}
}

// getNewChannel hits the server's /new endpoint to retrieve the channel_id.
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

// handleHTTPRequest reconstructs the received byte stream, sends it locally, and returns the result.
func handleHTTPRequest(
	ctx context.Context,
	msg TunnelMessage,
	wsConn *websocket.Conn,
	wsMutex *sync.Mutex,
	targetURL string,
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

	// Rewrite request information for the local server
	req.RequestURI = "" // NOTE: When sending as a client, it must be left blank.
	target, err := url.Parse(targetURL)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Target URL Parsing Error: %v", msg.ReqID, err))
		sendErrorResponse(msg.ReqID, wsConn, wsMutex)
		return
	}
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

	// Dump the received response as a raw byte stream
	rawRespBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		slog.Error(fmt.Sprintf("[ReqID: %s] Response Dump Error: %v", msg.ReqID, err))
		return
	}

	respMsg := TunnelMessage{
		ReqID:   msg.ReqID,
		Payload: rawRespBytes,
	}

	wsMutex.Lock()
	_ = wsConn.WriteJSON(respMsg) //nolint: errcheck
	wsMutex.Unlock()

	slog.Info(fmt.Sprintf("[ReqID: %s] The local response has been returned to the server. (Status: %d)", msg.ReqID, resp.StatusCode))
}

// sendErrorResponse returns a 502 Bad Gateway error when it cannot connect locally.
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
