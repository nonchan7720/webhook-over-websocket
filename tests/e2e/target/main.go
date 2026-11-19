// Command target is the local HTTP server that the E2E tests (tests/e2e/run.sh)
// forward webhooks to through the `client` subcommand.
//
// Every request is answered with a JSON echo of what the target received
// (method, path, raw query, host, selected headers and body), so the Postman
// collection can verify what went through the tunnel.
//
// Request headers that control the response:
//   - X-Echo-Status: response status code (default 200)
//   - X-Echo-Resp-Header: value of the X-Target-Header response header
//
// GET /large returns LargeSize bytes of deterministic ASCII text (see
// largeBody) instead of the JSON echo.
//
// With -free-ports N the command prints N free TCP ports (one per line) and
// exits; run.sh uses it to pick ports.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	largeSize   = 1 << 20 // 1 MiB
	lineWidth   = 64
	contentType = "Content-Type"
)

// echoResponse is the JSON body returned for normal requests.
type echoResponse struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	RawQuery string            `json:"raw_query"`
	Host     string            `json:"host"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
}

// largeBody builds the /large response: 64-byte lines "<8-digit line no> " +
// a run of the alphabet rotated by the line number, terminated by "\n".
// The Postman collection regenerates the same text to compare.
func largeBody() []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	var sb strings.Builder
	sb.Grow(largeSize)
	for i := 0; sb.Len() < largeSize; i++ {
		line := fmt.Sprintf("%08d ", i)
		for j := 0; len(line) < lineWidth-1; j++ {
			line += string(alphabet[(i+j)%len(alphabet)])
		}
		sb.WriteString(line + "\n")
	}
	return []byte(sb.String()[:largeSize])
}

func handler() http.Handler {
	large := largeBody()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Target-Marker", "e2e-target")
		if v := r.Header.Get("X-Echo-Resp-Header"); v != "" {
			w.Header().Set("X-Target-Header", v)
		}
		status := http.StatusOK
		if v := r.Header.Get("X-Echo-Status"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				status = n
			}
		}

		if r.URL.Path == "/large" {
			w.Header().Set(contentType, "text/plain")
			w.WriteHeader(status)
			// Write in uneven pieces to exercise chunked streaming.
			for off := 0; off < len(large); {
				n := min(10_000+off%7777, len(large)-off)
				if _, err := w.Write(large[off : off+n]); err != nil {
					return
				}
				off += n
			}
			return
		}

		resp := echoResponse{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Host:     r.Host,
			Headers: map[string]string{
				contentType:        r.Header.Get(contentType),
				"X-Custom-Request": r.Header.Get("X-Custom-Request"),
				"Content-Length":   strconv.FormatInt(r.ContentLength, 10),
			},
			Body: string(body),
		}
		w.Header().Set(contentType, "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp) //nolint: errcheck,errchkjson
	})
}

func printFreePorts(n int) error {
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close() //nolint: errcheck
		}
	}()
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		listeners = append(listeners, l)
		addr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			return errors.New("unexpected listener address")
		}
		fmt.Println(addr.Port)
	}
	return nil
}

func main() {
	port := flag.Int("port", 3000, "listen port")
	freePorts := flag.Int("free-ports", 0, "print N free TCP ports and exit")
	flag.Parse()

	if *freePorts > 0 {
		if err := printFreePorts(*freePorts); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background()) //nolint: errcheck
	}()
	log.Printf("target listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		os.Exit(1)
	}
}
