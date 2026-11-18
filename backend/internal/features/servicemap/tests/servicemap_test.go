package servicemap_tests

import (
	"fmt"
	"net/http"
	"testing"

	"obzly/internal/features/servicemap"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findNode(nodes []servicemap.ServiceMapNodeDTO, name string) *servicemap.ServiceMapNodeDTO {
	for i := range nodes {
		if nodes[i].Name == name {
			return &nodes[i]
		}
	}
	return nil
}

func findEdge(edges []servicemap.ServiceMapEdgeDTO, src, dst string) *servicemap.ServiceMapEdgeDTO {
	for i := range edges {
		if edges[i].Src == src && edges[i].Dst == dst {
			return &edges[i]
		}
	}
	return nil
}

func Test_GetServiceMap_MergesSpansOnlyAndNetworkOnlyEdgesIntoOneGraph(t *testing.T) {
	router := CreateServiceMapTestRouter()
	owner, project := SetUpProjectWithSpansAndNetworkMetrics(t, router, "Service Map Merge")

	var response servicemap.ServiceMapResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/service-map/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	// Nodes: gateway (both signals), payments (spans), postgres-db (network).
	gateway := findNode(response.Nodes, "gateway")
	require.NotNil(t, gateway)
	assert.ElementsMatch(t, []string{"spans", "network"}, gateway.Sources)
	assert.Equal(t, "prod", gateway.Namespace)
	assert.Equal(t, "gateway-deploy", gateway.Deployment)
	assert.Equal(t, "node-1", gateway.Node)

	payments := findNode(response.Nodes, "payments")
	require.NotNil(t, payments)
	assert.Equal(t, []string{"spans"}, payments.Sources)
	assert.Empty(t, payments.Namespace, "payments was seeded with no ResourceAttributes")

	postgres := findNode(response.Nodes, "postgres-db")
	require.NotNil(t, postgres)
	assert.Equal(t, []string{"network"}, postgres.Sources)
	// Network metrics carry a namespace but no pod/node label.
	assert.Equal(t, "prod", postgres.Namespace)
	assert.Empty(t, postgres.Node)
	assert.Empty(t, postgres.Deployment)
	// Network-only: no type, no RED numbers, no health.
	assert.Empty(t, postgres.Type)
	assert.Nil(t, postgres.Requests)
	assert.Empty(t, postgres.Health)
	assert.Equal(t, "service", gateway.Type)

	// Edge set: gateway->payments is spans-only (call count, no bytes).
	spansEdge := findEdge(response.Edges, "gateway", "payments")
	require.NotNil(t, spansEdge)
	assert.Equal(t, []string{"spans"}, spansEdge.Sources)
	require.NotNil(t, spansEdge.CallCount)
	assert.Equal(t, int64(1), *spansEdge.CallCount)
	assert.Nil(t, spansEdge.BytesTotal)
	require.NotNil(t, spansEdge.ErrorRate)
	assert.Equal(t, 0.0, *spansEdge.ErrorRate)

	// gateway->postgres-db is network-only: 3072-1024 = 2048 bytes.
	networkEdge := findEdge(response.Edges, "gateway", "postgres-db")
	require.NotNil(t, networkEdge)
	assert.Equal(t, []string{"network"}, networkEdge.Sources)
	require.NotNil(t, networkEdge.BytesTotal)
	assert.Equal(t, int64(2048), *networkEdge.BytesTotal)
	assert.Nil(t, networkEdge.CallCount)
	assert.Nil(t, networkEdge.ErrorRate)
}

func Test_GetServiceMap_NodesCarryRedHealthAndTypeFromSpans(t *testing.T) {
	router := CreateServiceMapTestRouter()
	owner, project := SetUpProjectWithRedSpans(t, router, "Service Map RED")

	var response servicemap.ServiceMapResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/service-map/%s", project.ID.String()),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)
	assert.InDelta(t, 24*3600, response.WindowSeconds, 5, "the default window is the 24 h lookback")

	// gateway: 2 root SERVER spans of 900 ms, no errors -> degraded by p95.
	gateway := findNode(response.Nodes, "gateway")
	require.NotNil(t, gateway)
	assert.Equal(t, "service", gateway.Type)
	require.NotNil(t, gateway.Requests)
	assert.Equal(t, int64(2), *gateway.Requests)
	assert.Equal(t, int64(0), *gateway.Errors)
	require.NotNil(t, gateway.ErrorRate)
	assert.Equal(t, 0.0, *gateway.ErrorRate)
	require.NotNil(t, gateway.P95LatencyMs)
	assert.InDelta(t, 900, *gateway.P95LatencyMs, 1)
	assert.Equal(t, "degraded", gateway.Health)

	// payments: 2 SERVER spans, one an error -> 50% -> failing. Its INTERNAL
	// span is not a request.
	payments := findNode(response.Nodes, "payments")
	require.NotNil(t, payments)
	assert.Equal(t, "service", payments.Type)
	assert.Equal(t, int64(2), *payments.Requests)
	assert.Equal(t, int64(1), *payments.Errors)
	assert.InDelta(t, 0.5, *payments.ErrorRate, 1e-9)
	assert.Equal(t, "failing", payments.Health)

	// Untraced peers named by client spans, typed from their attributes.
	ordersDB := findNode(response.Nodes, "orders-db")
	require.NotNil(t, ordersDB)
	assert.Equal(t, "database", ordersDB.Type)
	assert.Equal(t, []string{"spans"}, ordersDB.Sources)
	assert.Equal(t, int64(2), *ordersDB.Requests)
	assert.Equal(t, int64(0), *ordersDB.Errors)
	require.NotNil(t, ordersDB.P95LatencyMs)
	assert.InDelta(t, 10, *ordersDB.P95LatencyMs, 1)
	assert.Equal(t, "ok", ordersDB.Health)

	cache := findNode(response.Nodes, "cache-1")
	require.NotNil(t, cache)
	assert.Equal(t, "cache", cache.Type)

	kafka := findNode(response.Nodes, "kafka")
	require.NotNil(t, kafka, "a producer span with only messaging.system names the broker by it")
	assert.Equal(t, "messaging", kafka.Type)

	external := findNode(response.Nodes, "api.fabrikam.example")
	require.NotNil(t, external)
	assert.Equal(t, "external", external.Type)
	assert.Equal(t, int64(1), *external.Errors)
	assert.Equal(t, "failing", external.Health)

	// A client span answered by a traced server span is a normal edge, not a peer.
	assert.Nil(t, findNode(response.Nodes, "payments.prod.svc"))

	dbEdge := findEdge(response.Edges, "gateway", "orders-db")
	require.NotNil(t, dbEdge)
	assert.Equal(t, []string{"spans"}, dbEdge.Sources)
	require.NotNil(t, dbEdge.CallCount)
	assert.Equal(t, int64(2), *dbEdge.CallCount)
	require.NotNil(t, dbEdge.AvgLatencyMs)
	assert.InDelta(t, 10, *dbEdge.AvgLatencyMs, 1)

	tracedEdge := findEdge(response.Edges, "gateway", "payments")
	require.NotNil(t, tracedEdge)
	assert.Equal(t, int64(2), *tracedEdge.CallCount)
	require.NotNil(t, findEdge(response.Edges, "payments", "kafka"))
	require.NotNil(t, findEdge(response.Edges, "gateway", "api.fabrikam.example"))
}

func Test_GetServiceMap_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateServiceMapTestRouter()
	_, project := SetUpProjectWithSpansAndNetworkMetrics(t, router, "Service Map Forbidden")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/service-map/%s", project.ID.String()),
		"Bearer "+outsider.Token, http.StatusForbidden,
	)
}

func Test_GetServiceMap_InvalidProjectID_ReturnsBadRequest(t *testing.T) {
	router := CreateServiceMapTestRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		"/api/v1/service-map/not-a-uuid",
		"Bearer "+owner.Token, http.StatusBadRequest,
	)
}
