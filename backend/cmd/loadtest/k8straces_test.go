package main

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// A batch is exactly n spans of whole trees: every parent is in the batch,
// children run inside their parent, and requests cross several services.
func Test_K8sTraces_BatchIsTreesOfExactlyNSpans(t *testing.T) {
	services := map[string]*k8sService{}
	for _, svc := range k8sServices(40) {
		services[svc.name] = svc
	}
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := &simClock{start: start, perLine: float64(time.Second) / 2000}
	rng := rand.New(rand.NewSource(7))

	request := buildK8sTraceRequest(rng, services, clock, 0, 2000)

	spans := map[string]*tracev1.Span{}
	traceServices := map[string]map[string]bool{}
	var all []*tracev1.Span
	errors := 0
	for _, resource := range request.ResourceSpans {
		service := ""
		for _, attr := range resource.Resource.Attributes {
			if attr.Key == "service.name" {
				service = attr.Value.GetStringValue()
			}
		}
		require.Contains(t, services, service)
		for _, span := range resource.ScopeSpans[0].Spans {
			spans[string(span.SpanId)] = span
			all = append(all, span)
			if traceServices[string(span.TraceId)] == nil {
				traceServices[string(span.TraceId)] = map[string]bool{}
			}
			traceServices[string(span.TraceId)][service] = true
			if span.Status != nil && span.Status.Code == tracev1.Status_STATUS_CODE_ERROR {
				errors++
			}
		}
	}
	require.Len(t, all, 2000)
	for _, span := range all {
		require.LessOrEqual(t, span.StartTimeUnixNano, span.EndTimeUnixNano, span.Name)
		if len(span.ParentSpanId) == 0 {
			continue
		}
		parent, ok := spans[string(span.ParentSpanId)]
		require.True(t, ok, "the parent of %q is in the batch", span.Name)
		require.Equal(t, parent.TraceId, span.TraceId)
		require.GreaterOrEqual(t, span.StartTimeUnixNano, parent.StartTimeUnixNano, span.Name)
		require.LessOrEqual(t, span.EndTimeUnixNano, parent.EndTimeUnixNano, span.Name)
	}
	multi := 0
	for _, seen := range traceServices {
		if len(seen) >= 2 {
			multi++
		}
	}
	assert.Greater(t, multi, len(traceServices)*2/3, "most traces cross services")
	assert.Positive(t, errors, "some spans fail")
	assert.Less(t, errors, len(all)/5)
}
