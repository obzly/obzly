package inventory

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCollector struct {
	snapshots map[uuid.UUID]*snapshot
}

func (f *fakeCollector) collect(_ context.Context, projectID uuid.UUID, now time.Time) (*snapshot, error) {
	snap, ok := f.snapshots[projectID]
	if !ok {
		return &snapshot{now: now}, nil
	}
	snap.now = now
	return snap, nil
}

func Test_InventoryAPI_BuildThenRead_OnlyShowsTheUsersProjects(t *testing.T) {
	now := testNow
	testService := &Service{repo, func() time.Time { return now }}
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), &Controller{testService})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	other := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Inventory A", owner.Token, router)
	otherProject, _ := projects_testing.CreateTestProjectWithToken("Inventory B", other.Token, router)
	fake := &fakeCollector{snapshots: map[uuid.UUID]*snapshot{
		project.ID: clusterSnapshot(),
		otherProject.ID: {series: []metrics_core.SeriesLifetimeDTO{series("system_memory_usage",
			testNow.Add(-time.Hour), testNow, map[string]string{"host_name": "secret-host"})}},
	}}
	testBuilder := &Builder{repo, fake, slog.Default(), func() time.Time { return now }}
	require.NoError(t, testBuilder.Build(context.Background(), project.ID))
	require.NoError(t, testBuilder.Build(context.Background(), otherProject.ID))
	auth := "Bearer " + owner.Token

	var list AssetListDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/assets", auth, http.StatusOK, &list)
	byKey := map[string]AssetDTO{}
	for _, a := range list.Assets {
		byKey[a.Key] = a
		assert.Equal(t, project.ID, a.ProjectID, "only the user's own projects")
	}
	require.Contains(t, byKey, "pod:prd/shop/uid-checkout-1")
	assert.Equal(t, "Inventory A", byKey["pod:prd/shop/uid-checkout-1"].ProjectName)
	assert.Equal(t, HealthCritical, list.Assets[0].Health, "worst first")
	assert.Equal(t, int64(len(list.Assets)), list.Total)

	var pods AssetListDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/assets?kind=pod&health=warning&q=checkout",
		auth, http.StatusOK, &pods)
	require.Len(t, pods.Assets, 1)
	assert.Equal(t, "checkout-api-7d9f8b6c5d-x7k2p", pods.Assets[0].Name)

	test_utils.MakeGetRequest(t, router, "/api/v1/inventory/assets?projectId="+otherProject.ID.String(), auth,
		http.StatusForbidden)
	var otherList AssetListDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/assets", "Bearer "+other.Token,
		http.StatusOK, &otherList)
	require.Len(t, otherList.Assets, 1)
	test_utils.MakeGetRequest(t, router, "/api/v1/inventory/assets/"+otherList.Assets[0].ID.String(), auth,
		http.StatusNotFound)

	var detail AssetDetailDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router,
		"/api/v1/inventory/assets/"+byKey["pod:prd/shop/uid-checkout-1"].ID.String(), auth, http.StatusOK, &detail)
	var parents []string
	for _, p := range detail.Parents {
		parents = append(parents, p.Kind+" "+p.Name)
	}
	assert.Equal(t, []string{"cluster prd", "namespace shop", "workload checkout-api"}, parents)
	require.NotNil(t, detail.Node)
	assert.Equal(t, "ip-10-0-1-5.ec2.internal", detail.Node.Name)
	require.Len(t, detail.Children, 1)
	assert.Equal(t, "app", detail.Children[0].Name)

	var summary SummaryDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/summary", auth, http.StatusOK, &summary)
	assert.Equal(t, int64(len(list.Assets)), summary.Total)
	assert.Equal(t, int64(4), sum(summary.Kinds[KindPod]))
	require.Len(t, summary.Projects, 1)
	assert.Equal(t, ProjectCountDTO{ID: project.ID, Name: "Inventory A", Assets: summary.Total,
		Live: summary.Projects[0].Live}, summary.Projects[0])
	assert.Equal(t, summary.Live, summary.Projects[0].Live, "one project: its live counts are the total's")

	// A later build records what changed.
	now = testNow.Add(15 * time.Minute)
	later := clusterSnapshot()
	later.series = append(later.series, series("k8s_pod_memory_working_set", now.Add(-time.Minute), now,
		map[string]string{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
			"k8s_pod_name": "checkout-api-7d9f8b6c5d-new99", "k8s_pod_uid": "uid-new"}))
	fake.snapshots[project.ID] = later
	require.NoError(t, testBuilder.Build(context.Background(), project.ID))
	var changes []ChangeDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/changes", auth, http.StatusOK, &changes)
	var sawNew, sawNotReporting bool
	for _, c := range changes {
		sawNew = sawNew || c.AssetKey == "pod:prd/shop/uid-new" && c.Type == ChangeNew
		sawNotReporting = sawNotReporting || c.Type == ChangeState && c.To == StateNotReporting
		assert.Equal(t, "Inventory A", c.ProjectName)
	}
	assert.True(t, sawNew, "the new pod is in the feed")
	assert.True(t, sawNotReporting, "assets that stopped reporting are in the feed")
}

func Test_InventorySummary_WithHistory_ReturnsHourlyCountsAndThoseOfADayAgo(t *testing.T) {
	now := testNow
	testService := &Service{repo, func() time.Time { return now }}
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), &Controller{testService})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Inventory history", owner.Token, router)
	auth := "Bearer " + owner.Token
	summaryOf := func() SummaryDTO {
		var summary SummaryDTO
		test_utils.MakeGetRequestAndUnmarshal(t, router,
			"/api/v1/inventory/summary?history=true&projectId="+project.ID.String(), auth, http.StatusOK, &summary)
		return summary
	}

	empty := summaryOf()
	require.NotNil(t, empty.History)
	assert.Empty(t, empty.History.Hours, "no build yet: no history, not zeros")
	assert.Nil(t, empty.History.PreviousAt)

	fake := &fakeCollector{snapshots: map[uuid.UUID]*snapshot{project.ID: clusterSnapshot()}}
	testBuilder := &Builder{repo, fake, slog.Default(), func() time.Time { return now }}
	require.NoError(t, testBuilder.Build(context.Background(), project.ID))

	// A day and an hour later a fifth pod appears; two builds in that hour keep one count.
	now = testNow.Add(25 * time.Hour)
	later := clusterSnapshot()
	later.series = append(later.series, series("k8s_pod_memory_working_set", now.Add(-time.Minute), now,
		map[string]string{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
			"k8s_pod_name": "checkout-api-7d9f8b6c5d-new99", "k8s_pod_uid": "uid-new"}))
	fake.snapshots[project.ID] = later
	require.NoError(t, testBuilder.Build(context.Background(), project.ID))
	now = testNow.Add(25*time.Hour + 10*time.Minute)
	require.NoError(t, testBuilder.Build(context.Background(), project.ID))

	summary := summaryOf()
	history := summary.History
	require.NotNil(t, history)
	require.Len(t, history.Hours, 2)
	assert.True(t, history.Hours[0].Equal(testNow))
	assert.True(t, history.Hours[1].Equal(testNow.Add(25*time.Hour)))
	assert.Equal(t, []int64{4, 5}, history.Kinds[KindPod])
	assert.Equal(t, sum(summary.Kinds[KindPod]), history.Kinds[KindPod][1], "the newest hour matches the summary")
	require.NotNil(t, history.PreviousAt)
	assert.True(t, history.PreviousAt.Equal(testNow), "the newest hour at least a day old")
	assert.Equal(t, int64(4), sum(history.Previous[KindPod]))

	// Three days on, nothing is close enough to a day old to compare with.
	now = testNow.Add(72 * time.Hour)
	assert.Nil(t, summaryOf().History.PreviousAt)

	var plain SummaryDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/inventory/summary", auth, http.StatusOK, &plain)
	assert.Nil(t, plain.History, "only with history=true")

	require.NoError(t, repo.deleteCountsBefore(testNow.Add(time.Hour)))
	now = testNow.Add(25 * time.Hour)
	assert.Len(t, summaryOf().History.Hours, 1, "counts past retention are deleted")
}

func sum(counts map[string]int64) int64 {
	var total int64
	for _, c := range counts {
		total += c
	}
	return total
}
