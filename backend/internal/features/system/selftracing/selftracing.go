// Package system_selftracing records a sample of Obzly's own requests and background
// passes as traces in the Site's reserved system project.
//
// Spans are written straight into the local spans store by an in-process exporter,
// never over HTTP/OTLP, so storing them produces no traceable request (the loop guard).
// Entry points start root spans and never adopt an incoming traceparent, so a client
// cannot force sampling.
package system_selftracing

import (
	"context"
	"os"

	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ServiceName is the service.name every self-trace carries.
const ServiceName = "obzly"

// Tracing is the installed process-wide tracer provider.
type Tracing struct {
	provider *sdktrace.TracerProvider
}

// Options configures Start.
type Options struct {
	// ProjectID: the system project spans are stored in.
	ProjectID uuid.UUID
	// SampleRate: fraction of root spans kept, 0..1.
	SampleRate float64
	// Attributes are added to the resource (version, service mode, Site).
	Attributes []attribute.KeyValue
}

// Start installs a tracer provider that samples opts.SampleRate of root
// spans and batches them into the spans store. Call Shutdown before the
// final segment flush so buffered spans are written.
func Start(opts Options) *Tracing {
	store := func(spans []*spans_core.Span) error {
		return spans_core.GetSpanCoreRepository().
			StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{opts.ProjectID: spans})
	}
	return startWithStore(opts, store)
}

// Shutdown flushes buffered spans into the store and stops tracing.
func (t *Tracing) Shutdown(ctx context.Context) error {
	return t.provider.Shutdown(ctx)
}

func startWithStore(opts Options, store func([]*spans_core.Span) error) *Tracing {
	attributes := []attribute.KeyValue{attribute.String("service.name", ServiceName)}
	if hostname, err := os.Hostname(); err == nil {
		attributes = append(attributes, attribute.String("host.name", hostname))
	}
	attributes = append(attributes, opts.Attributes...)

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRate))),
		sdktrace.WithResource(resource.NewSchemaless(attributes...)),
		sdktrace.WithBatcher(&spanExporter{projectID: opts.ProjectID, store: store}),
	)
	otel.SetTracerProvider(provider)
	return &Tracing{provider: provider}
}
