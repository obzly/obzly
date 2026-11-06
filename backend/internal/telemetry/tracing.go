package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName identifies Obzly's own instrumentation. otel.Tracer resolves the
// global provider on every call, so spans follow whatever
// system_selftracing.Start installed; until then it is a no-op.
const tracerName = "obzly"

// StartRootSpan starts a span that is always a new trace root: self-tracing
// never adopts a caller's traceparent, so an external client cannot force
// Obzly to sample (and store) its requests.
func StartRootSpan(
	ctx context.Context,
	name string,
	kind trace.SpanKind,
	attributes ...attribute.KeyValue,
) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name,
		trace.WithNewRoot(), trace.WithSpanKind(kind), trace.WithAttributes(attributes...))
}
