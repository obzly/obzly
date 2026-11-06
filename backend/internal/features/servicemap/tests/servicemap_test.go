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
