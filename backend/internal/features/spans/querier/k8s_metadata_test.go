package spans_querier_test

import (
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ServiceK8sMetadata_ExtractsFromResourceAttributes(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			TraceID: "trace-a", SpanID: "s1", Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(10 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
			ResourceAttributes: map[string]any{
				"k8s.namespace.name":  "prod",
				"k8s.deployment.name": "gateway-deploy",
				"k8s.node.name":       "node-1",
			},
		},
		{
			// No ResourceAttributes: must not appear in the result.
			TraceID: "trace-a", SpanID: "s2", ParentSpanID: "s1", Name: "POST /charge",
			ServiceName: "payments", StartTime: base.Add(1 * time.Millisecond),
			EndTime: base.Add(5 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
		},
	}
	projectID, querier := seedProject(t, spanList)

	metadata, err := querier.ServiceK8sMetadata(projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)))
	require.NoError(t, err)

	gateway, ok := metadata["gateway"]
	require.True(t, ok)
	assert.Equal(t, "prod", gateway.Namespace)
	assert.Equal(t, "gateway-deploy", gateway.Deployment)
	assert.Equal(t, "node-1", gateway.Node)

	_, ok = metadata["payments"]
	assert.False(t, ok, "a service with no ResourceAttributes must not appear in the result")
}

func Test_ServiceK8sMetadata_MultipleSpans_PicksMostRecentPerField(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Millisecond)
	spanList := []*spans_core.Span{
		{
			TraceID: "trace-a", SpanID: "s1", Name: "GET /a", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(10 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
			ResourceAttributes: map[string]any{
				"k8s.namespace.name": "prod",
				"k8s.node.name":      "node-old",
			},
		},
		{
			// Same service, later span, different node: the later one wins.
			TraceID: "trace-b", SpanID: "s2", Name: "GET /b", ServiceName: "gateway",
			StartTime: base.Add(time.Minute), EndTime: base.Add(time.Minute + 10*time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
			ResourceAttributes: map[string]any{
				"k8s.namespace.name": "prod",
				"k8s.node.name":      "node-new",
			},
		},
	}
	projectID, querier := seedProject(t, spanList)

	metadata, err := querier.ServiceK8sMetadata(
		projectID, timeRange(base.Add(-time.Minute), base.Add(time.Hour)),
	)
	require.NoError(t, err)

	gateway, ok := metadata["gateway"]
	require.True(t, ok)
	assert.Equal(t, "node-new", gateway.Node)
}
