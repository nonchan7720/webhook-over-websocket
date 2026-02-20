package middlewares

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type Skipper func(r *http.Request) bool

func Logging(skipper Skipper) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipper != nil && skipper(r) {
				h.ServeHTTP(w, r)
				return
			}
			url := r.URL.EscapedPath()
			requestId := getRequestId(r.Header)
			requestIdWith := slog.String("request-id", requestId)
			start := time.Now()
			requestWith := []any{
				requestIdWith,
				slog.Time("request-time", start),
				slog.String("request-path", url),
			}
			log := slog.With(requestWith...)
			log.Info(fmt.Sprintf("request calling: %s", url))
			h.ServeHTTP(w, r)
			end := time.Now()
			latency := end.Sub(start)
			log = log.With(
				slog.Time("response-time", end),
				slog.String("latency", secToTime(latency)),
			)
			log.Info(fmt.Sprintf("response calling: %s", url))
		})
	}
}

func getRequestId(header http.Header) string {
	value := header.Get("x-request-id")
	if value == "" {
		return uuid.Must(uuid.NewV7()).String()
	}
	return value
}

func secToTime(sec time.Duration) string {
	const format = "15:04:05.000"
	tZero := time.Unix(0, 0).UTC()
	return tZero.Add(sec).Format(format)
}
