// Command redis runs an in-memory Redis-compatible server (miniredis) for the
// Pub/Sub (--cluster-backend redis) phase of the E2E tests (tests/e2e/run.sh).
//
// NOTE: miniredis does not expire keys by wall-clock time (TTLs only move with
// FastForward), so expiry-based behaviour is not covered by the E2E tests.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/alicebob/miniredis/v2"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6379", "listen address")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	m := miniredis.NewMiniRedis()
	if err := m.StartAddr(*addr); err != nil {
		log.Printf("failed to start miniredis: %v", err)
		return
	}
	defer m.Close()
	log.Printf("miniredis listening on %s", m.Addr())
	<-ctx.Done()
}
