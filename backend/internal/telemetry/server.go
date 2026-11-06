package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// unmatchedRoute labels requests no gin route matched (the SPA fallback,
// 404s), so arbitrary paths never become label values.
const unmatchedRoute = "unmatched"

// Listen binds addr up front so a bad METRICS_ADDR or a port clash fails at
// startup, like the HTTP and OTLP gRPC listeners do.
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// Serve exposes Registry at /metrics on listener. It is a separate server
// from the public one so a NetworkPolicy or security group can close it to
// everything but the scraper.
func Serve(listener net.Listener, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry}))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server stopped unexpectedly", "error", err)
		}
	}()
	return srv
}

// Shutdown stops a server returned by Serve; nil is a no-op.
func Shutdown(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// RegisterDBStats exports the Postgres connection pool (open, in use, waits)
// - N replicas x pool size against max_connections is a known scaling limit.
func RegisterDBStats(db *sql.DB) {
	Registry.MustRegister(collectors.NewDBStatsCollector(db, "postgres"))
}

// GinMiddleware records HTTP latency keyed by the route template
// (ctx.FullPath), never the raw path, and a self-tracing root span with the
// method, route and status only - no query string (it can carry api_key),
// headers or client address.
func GinMiddleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		route := ctx.FullPath()
		if route == "" {
			route = unmatchedRoute
		}
		method := ctx.Request.Method

		spanCtx, span := StartRootSpan(ctx.Request.Context(), method+" "+route, trace.SpanKindServer,
			attribute.String("http.request.method", method), attribute.String("http.route", route))
		ctx.Request = ctx.Request.WithContext(spanCtx)

		ctx.Next()

		status := ctx.Writer.Status()
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
		span.End()

		HTTPRequestDuration.
			WithLabelValues(method, route, strconv.Itoa(status)).
			Observe(time.Since(start).Seconds())
	}
}

// UnaryServerInterceptor records OTLP gRPC latency by full method name and
// status code.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		ctx, span := StartRootSpan(ctx, info.FullMethod, trace.SpanKindServer,
			attribute.String("rpc.system", "grpc"), attribute.String("rpc.method", info.FullMethod))
		resp, err := handler(ctx, req)

		code := status.Code(err)
		span.SetAttributes(attribute.String("rpc.grpc.status_code", code.String()))
		if err != nil {
			span.SetStatus(codes.Error, code.String())
		}
		span.End()

		GRPCRequestDuration.
			WithLabelValues(info.FullMethod, code.String()).
			Observe(time.Since(start).Seconds())
		return resp, err
	}
}
