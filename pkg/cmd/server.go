package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nonchan7720/webhook-over-websocket/pkg/cluster"
	"github.com/nonchan7720/webhook-over-websocket/pkg/cmd/args"
	"github.com/nonchan7720/webhook-over-websocket/pkg/handler"
	"github.com/nonchan7720/webhook-over-websocket/pkg/middlewares"
	"github.com/nonchan7720/webhook-over-websocket/pkg/utils"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TunnelMessage is the message format shared by the server and the client over WebSocket.
type TunnelMessage = handler.TunnelMessage

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

// tunnelServer is the HTTP side of the server, which differs by cluster backend.
type tunnelServer interface {
	BuildMux() *http.ServeMux
	CleanNonActiveSession()
}

// newServer creates the cluster backend selected by --cluster-backend and the handler for it.
// The returned cluster must be started by the caller.
func newServer(serverCtx context.Context, cfg *args.Server, myIP string) (cluster.Cluster, tunnelServer, error) {
	opts := handler.Options{
		MyIP:      myIP,
		Port:      cfg.Port,
		ServerCtx: serverCtx,

		WebhookMaxBodySize: cfg.WebhookMaxBodySize,

		AuthEnabled:        cfg.AuthEnabled(),
		JwtSecret:          []byte(cfg.JwtSigningKey),
		GithubClientID:     cfg.GithubClientID,
		GithubClientSecret: cfg.GithubClientSecret,
		GithubOrg:          cfg.GithubOrg,
	}
	switch cfg.ClusterBackend {
	case args.ClusterBackendRedis:
		// Nodes cannot reach each other directly, so all traffic goes through redis.
		relay, err := cluster.NewRedis(cluster.RedisConfig{
			URL:                   cfg.RedisURL,
			KeyPrefix:             cfg.RedisKeyPrefix,
			HeartbeatInterval:     cfg.RedisHeartbeatDuration,
			ClusterMode:           cfg.RedisClusterMode,
			TLSCAFile:             cfg.RedisTLSCAFile,
			TLSInsecureSkipVerify: cfg.RedisTLSInsecureSkipVerify,
		})
		if err != nil {
			return nil, nil, err
		}
		return relay, handler.NewPubSubHandler(opts, relay), nil
	default:
		mlist, err := cluster.NewMemberlist(cluster.MemberlistConfig{
			BindPort:     cfg.MemberListPort,
			MyIP:         myIP,
			ServerPort:   cfg.Port,
			PeerDomain:   cfg.PeerDomain,
			SyncDuration: cfg.MemberlistSyncDuration,
		})
		if err != nil {
			return nil, nil, err
		}
		return mlist, handler.NewMemberlistHandler(opts, mlist), nil
	}
}

func executeServer(ctx context.Context, args *args.Server) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	// Validate auth configuration
	if err := args.Validate(); err != nil {
		return err
	}
	myIP := handler.GetLocalIP()

	c, h, err := newServer(serverCtx, args, myIP)
	if err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", args.Port))
	if err != nil {
		return err
	}
	mux := h.BuildMux()
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
			h.CleanNonActiveSession()
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
