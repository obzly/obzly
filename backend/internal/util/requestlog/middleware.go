// Package requestlog replaces gin's default access log with structured slog entries.
// Gin's logger writes the raw query string (leaking ?api_key=... values) and
// concatenates the path into one line (an encoded newline could forge log lines);
// slog fields avoid both, and sensitive query params are redacted.
package requestlog

import (
	"log/slog"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// sensitiveQueryParams are redacted before the query string is logged.
var sensitiveQueryParams = map[string]struct{}{
	"api_key": {},
	"apiKey":  {},
}

// Middleware logs one structured entry per request, replacing gin.Logger(). Use it with
// gin.New() plus gin.Recovery().
func Middleware(log *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		path := ctx.Request.URL.Path
		query := redactedQuery(ctx.Request.URL.Query())

		ctx.Next()

		log.Info("request",
			slog.String("method", ctx.Request.Method),
			slog.String("path", path),
			slog.String("query", query),
			slog.Int("status", ctx.Writer.Status()),
			slog.Duration("duration", time.Since(start)),
			slog.String("clientIP", ctx.ClientIP()),
		)
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
