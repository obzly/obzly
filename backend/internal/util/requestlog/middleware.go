// Package requestlog replaces gin's default access log with structured slog entries.
// Gin's logger writes the raw query string (leaking ?api_key=... values) and
// concatenates the path into one line (an encoded newline could forge log lines);
// slog fields avoid both, and sensitive query params are redacted.
package requestlog

import (
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// sensitiveQueryParams are redacted before the query string is logged.
var sensitiveQueryParams = map[string]struct{}{
	"api_key": {},
	"apiKey":  {},
}

// slowRequest: a routine request this slow is logged at Info anyway.
const slowRequest = time.Second

// routinePrefixes are frequent requests (OTLP ingest, probes and health
// checks) logged at Debug when they succeed in time: a busy Site's API pod
// logged ~1,150 ingest calls a minute, rotating everything else out of its
// log within minutes.
var routinePrefixes = []string{"/v1/", "/api/v1/system/live", "/api/v1/system/ready", "/api/v1/system/health"}

// Middleware logs one structured entry per request, replacing gin.Logger(). Use it with
// gin.New() plus gin.Recovery().
func Middleware(log *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		path := ctx.Request.URL.Path
		query := redactedQuery(ctx.Request.URL.Query())

		ctx.Next()

		took := time.Since(start)
		log.Log(ctx.Request.Context(), requestLevel(path, ctx.Writer.Status(), took), "request",
			slog.String("method", ctx.Request.Method),
			slog.String("path", path),
			slog.String("query", query),
			slog.Int("status", ctx.Writer.Status()),
			slog.Duration("duration", took),
			slog.String("clientIP", ctx.ClientIP()),
		)
	}
}

// requestLevel is Warn for a server error, Debug for a routine request that
// succeeded in time, else Info.
func requestLevel(path string, status int, took time.Duration) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelWarn
	case status < http.StatusBadRequest && took < slowRequest &&
		slices.ContainsFunc(routinePrefixes, func(prefix string) bool { return strings.HasPrefix(path, prefix) }):
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// redactedQuery re-encodes values with every sensitiveQueryParams value replaced.
func redactedQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	redacted := make(url.Values, len(values))
	for key, vals := range values {
		if _, sensitive := sensitiveQueryParams[key]; sensitive {
			redacted[key] = []string{"[REDACTED]"}
			continue
		}
		redacted[key] = vals
	}
	return redacted.Encode()
}
