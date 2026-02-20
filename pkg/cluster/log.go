package cluster

import (
	"io"
	"log/slog"
	"strings"
)

type slogWriter struct{}

var (
	_ io.Writer = (*slogWriter)(nil)
)

func (*slogWriter) Write(p []byte) (n int, err error) {
	val := string(p)
	switch {
	case strings.Contains(val, "[DEBUG]"):
		slog.Debug(val)
	case strings.Contains(val, "[INFO]"):
		slog.Info(val)
	case strings.Contains(val, "[WARN]"):
		slog.Warn(val)
	case strings.Contains(val, "[ERR]"):
		slog.Error(val)
	}
	return len(p), nil
}
