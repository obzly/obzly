package servicemap

import (
	"fmt"
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseRFC3339(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}

func Test_mergeServiceMap_BothSourcesOnSameEdge_TaggedBoth(t *testing.T) {
	spanEdges := []spans_core.ServiceCallEdge{
		{SrcService: "a", DstService: "b", CallCount: 10, ErrorCount: 2},
	}
	netEdges := []NetworkEdge{
		{SrcService: "a", DstService: "b", BytesTotal: 500, PacketsTotal: 3},
	}

	response := mergeServiceMap(spanEdges, netEdges, nil)

	require.Len(t, response.Edges, 1)
	edge := response.Edges[0]
	assert.ElementsMatch(t, []string{"spans", "network"}, edge.Sources)
	require.NotNil(t, edge.CallCount)
	assert.Equal(t, int64(10), *edge.CallCount)
	require.NotNil(t, edge.ErrorRate)
	assert.Equal(t, 0.2, *edge.ErrorRate)
	require.NotNil(t, edge.BytesTotal)
	assert.Equal(t, int64(500), *edge.BytesTotal)

	require.Len(t, response.Nodes, 2)
}

func Test_mergeServiceMap_K8sMetadata_AttachedByName(t *testing.T) {
	spanEdges := []spans_core.ServiceCallEdge{
		{SrcService: "gateway", DstService: "payments", CallCount: 5},
	}
	netEdges := []NetworkEdge{
		// Network-only, yet it has a namespace from the network metrics.
		{SrcService: "gateway", DstService: "postgres-db", BytesTotal: 100, PacketsTotal: 1},
	}
	k8sMetadata := map[string]k8sInfo{
		"gateway":     {Namespace: "prod", Deployment: "gateway-deploy", Node: "node-1"},
		"payments":    {Namespace: "prod", Deployment: "payments-deploy", Node: "node-2"},
		"postgres-db": {Namespace: "prod"}, // no Deployment/Node - OBI's network metrics never provide these
	}

	response := mergeServiceMap(spanEdges, netEdges, k8sMetadata)

	byName := make(map[string]ServiceMapNodeDTO, len(response.Nodes))
	for _, node := range response.Nodes {
		byName[node.Name] = node
	}

	gateway := byName["gateway"]
	assert.Equal(t, "prod", gateway.Namespace)
	assert.Equal(t, "gateway-deploy", gateway.Deployment)
	assert.Equal(t, "node-1", gateway.Node)

	payments := byName["payments"]
	assert.Equal(t, "payments-deploy", payments.Deployment)

	postgres := byName["postgres-db"]
	assert.Equal(t, "prod", postgres.Namespace)
	assert.Empty(t, postgres.Node)
	assert.Empty(t, postgres.Deployment)
}

func Test_mergeK8sMetadata_SpansWinsOverNetwork_NetworkFillsGaps(t *testing.T) {
	spanMetadata := map[string]spans_core.K8sResourceInfo{
		"gateway": {Namespace: "prod", Deployment: "gateway-deploy", Node: "node-1"},
	}
	networkMetadata := map[string]networkK8sInfo{
		// Disagrees with spans: spans must win.
		"gateway": {Namespace: "staging"},
		// Network metrics are the only source.
		"postgres-db": {Namespace: "prod"},
	}

	merged := mergeK8sMetadata(spanMetadata, networkMetadata)

	gateway := merged["gateway"]
	assert.Equal(t, "prod", gateway.Namespace, "spans must win over a disagreeing network-metrics value")
	assert.Equal(t, "node-1", gateway.Node)
	assert.Equal(t, "gateway-deploy", gateway.Deployment, "network metrics have no deployment concept at all")

	postgres := merged["postgres-db"]
	assert.Equal(t, "prod", postgres.Namespace, "network metrics must fill in a service spans never saw")
	assert.Empty(t, postgres.Node)
	assert.Empty(t, postgres.Deployment)
}

func Test_mergeServiceMap_OverCap_CollapsesIntoOtherNode(t *testing.T) {
	// root plus maxRankedNodes leaves: maxRankedNodes-1 are kept (root
	// among them, it has the most volume), so the 2 quietest leaves collapse.
	spanEdges := make([]spans_core.ServiceCallEdge, 0, maxRankedNodes)
	for i := 0; i < maxRankedNodes; i++ {
		spanEdges = append(spanEdges, spans_core.ServiceCallEdge{
			SrcService: "root",
			DstService: fmt.Sprintf("svc-%02d", i),
			CallCount:  int64(maxRankedNodes - i),
		})
	}

	response := mergeServiceMap(spanEdges, nil, nil)

	names := make([]string, 0, len(response.Nodes))
	for _, node := range response.Nodes {
		names = append(names, node.Name)
	}
	assert.Contains(t, names, otherNodeName)
	// The two lowest-call-count leaves (call counts 1 and 2) collapse.
	assert.NotContains(t, names, fmt.Sprintf("svc-%02d", maxRankedNodes-1))
	assert.NotContains(t, names, fmt.Sprintf("svc-%02d", maxRankedNodes-2))
	assert.Contains(t, names, fmt.Sprintf("svc-%02d", maxRankedNodes-3))

	otherEdge := findEdgeInTest(response.Edges, "root", otherNodeName)
	require.NotNil(t, otherEdge)
	require.NotNil(t, otherEdge.CallCount)
	assert.Equal(t, int64(1+2), *otherEdge.CallCount)
}

func findEdgeInTest(edges []ServiceMapEdgeDTO, src, dst string) *ServiceMapEdgeDTO {
	for i := range edges {
		if edges[i].Src == src && edges[i].Dst == dst {
			return &edges[i]
		}
	}
	return nil
}

func Test_clampTimeRange_WiderThanMax_ClampsFrom(t *testing.T) {
	to := mustParseRFC3339(t, "2026-01-10T00:00:00Z")
	from := mustParseRFC3339(t, "2026-01-01T00:00:00Z") // 9 days, wider than the 24h max

	resolvedFrom, resolvedTo := clampTimeRange(&from, &to)

	assert.Equal(t, to, resolvedTo)
	assert.Equal(t, to.Add(-maxServiceMapLookback), resolvedFrom)
}

func Test_clampTimeRange_NarrowerThanMax_LeftUntouched(t *testing.T) {
	to := mustParseRFC3339(t, "2026-01-10T00:00:00Z")
	from := to.Add(-time.Hour)

	resolvedFrom, resolvedTo := clampTimeRange(&from, &to)

	assert.Equal(t, to, resolvedTo)
	assert.Equal(t, from, resolvedFrom)
}
