package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func echoCommand() *cobra.Command {
	var (
		port int
	)
	cmd := &cobra.Command{
		Use:           "echo",
		SilenceErrors: true,
		SilenceUsage:  true,
		Run: func(cmd *cobra.Command, args []string) {
			executeEchoServer(cmd.Context(), port)
		},
	}
	flag := cmd.Flags()
	flag.IntVarP(&port, "port", "p", 3000, "echo server port")
	return cmd
}

func executeEchoServer(ctx context.Context, port int) {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, value := range r.Header {
			for _, v := range value {
				w.Header().Add(key, v)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, r.Body) //nolint: errcheck
	}))
	srv := &http.Server{
		Handler:           mux,
		Addr:              fmt.Sprintf(":%d", port),
		ReadHeaderTimeout: 20 * time.Second,
	}
	slog.Info(fmt.Sprintf("Start echo server :%d", port))
	go func() {
		if err := srv.ListenAndServe(); err != nil && errors.Is(err, http.ErrServerClosed) {
			slog.Error(err.Error())
		}
	}()
	<-ctx.Done()
	slog.Info("Stop echo server")
}
