// Package bodylimit caps request bodies on API routes that don't set a
// limit of their own, so a large JSON body can't make a handler buffer
// and decode unbounded input.
package bodylimit

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const routeLimitKey = "bodylimit.route"

// lazyLimit applies the limit on the first read, once the route has had
// its chance to set its own.
type lazyLimit struct {
	ctx    *gin.Context
	body   io.ReadCloser
	limit  int64
	reader io.ReadCloser
}

// Middleware caps every request body at limit bytes, unless the route
// sets its own limit with Route or Set before the body is read. A body
// over the limit fails to read (*http.MaxBytesError).
func Middleware(limit int64) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if ctx.Request.Body != nil && ctx.Request.Body != http.NoBody {
			ctx.Request.Body = &lazyLimit{ctx: ctx, body: ctx.Request.Body, limit: limit}
		}
		ctx.Next()
	}
}

// Route gives a route its own body limit in place of Middleware's.
func Route(limit int64) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		Set(ctx, limit)
		ctx.Next()
	}
}

// Set gives this request its own body limit; call it before reading.
func Set(ctx *gin.Context, limit int64) {
	ctx.Set(routeLimitKey, limit)
}

func (l *lazyLimit) Read(p []byte) (int, error) {
	if l.reader == nil {
		limit := l.limit
		if value, ok := l.ctx.Get(routeLimitKey); ok {
			if own, ok := value.(int64); ok {
				limit = own
			}
		}
		l.reader = http.MaxBytesReader(l.ctx.Writer, l.body, limit)
	}
	return l.reader.Read(p)
}

func (l *lazyLimit) Close() error {
	return l.body.Close()
}
