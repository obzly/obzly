package inventory

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagingFixture is a user with two projects: a small cluster in "Inventory paging" and one host in
// "Contoso shop".
type pagingFixture struct {
	router  *gin.Engine
	auth    string
	project uuid.UUID
	other   uuid.UUID
}

// byHealth is the fixture's assets in the default order: health, then name.
var byHealth = []string{
	"checkout-api", "checkout-api-1", // critical
	"app", "shop", // warning
	"build-01", "checkout-api-2", "ip-10-0-1-5", "prd", // healthy
	"old-job", // inactive
}

func Test_ListAssets_WithOffsetAndLimit_ReturnsConsecutivePagesAndTheTotal(t *testing.T) {
	f := newPagingFixture(t)

	var names []string
	for offset := 0; offset < 12; offset += 4 {
		list := f.list(t, "sort=name&limit=4&offset="+strconv.Itoa(offset))
		assert.Equal(t, int64(9), list.Total, "total counts every match, not the page")
		assert.LessOrEqual(t, len(list.Assets), 4)
		names = append(names, namesOf(list.Assets)...)
	}
	assert.Equal(t, []string{"app", "build-01", "checkout-api", "checkout-api-1", "checkout-api-2",
		"ip-10-0-1-5", "old-job", "prd", "shop"}, names)

	past := f.list(t, "offset=50&limit=10")
	assert.Empty(t, past.Assets)
	assert.Equal(t, int64(9), past.Total)
}

func Test_ListAssets_WithoutPagingParams_KeepsTheOldLimitBehaviour(t *testing.T) {
	f := newPagingFixture(t)

	all := f.list(t, "")
	assert.Equal(t, byHealth, namesOf(all.Assets), "worst health first, then name")
	assert.Equal(t, int64(9), all.Total)

	capped := f.list(t, "limit=3")
	assert.Equal(t, byHealth[:3], namesOf(capped.Assets))
	assert.Equal(t, int64(9), capped.Total, "total says more matched than were returned")
	assert.Nil(t, capped.Ancestors)
	assert.Nil(t, capped.Assets[0].ChildCount, "child counts only when asked for")

	huge := f.list(t, "limit=999999")
	assert.Len(t, huge.Assets, 9)
}

func Test_ListAssets_WithSort_OrdersEveryColumnBothWays(t *testing.T) {
	f := newPagingFixture(t)

	assert.Equal(t, byHealth, namesOf(f.list(t, "sort=health&order=asc").Assets))
	reversed := make([]string, len(byHealth))
	for i, name := range byHealth {
		reversed[len(byHealth)-1-i] = name
	}
	assert.Equal(t, reversed, namesOf(f.list(t, "sort=health&order=desc").Assets), "desc reverses every key")

	assert.Equal(t, []string{"prd", "ip-10-0-1-5", "build-01", "shop", "checkout-api",
		"checkout-api-1", "checkout-api-2", "old-job", "app"}, namesOf(f.list(t, "sort=kind").Assets),
		"kinds in the hierarchy order")

	assert.Equal(t, []string{"checkout-api-1", "app", "ip-10-0-1-5", "checkout-api-2",
		"shop", "prd", "old-job", "checkout-api", "build-01"}, namesOf(f.list(t, "sort=cpu&order=desc").Assets),
		"most CPU first, assets without a value last")
	assert.Equal(t, []string{"build-01", "checkout-api"},
		namesOf(f.list(t, "sort=cpu&order=asc&limit=2").Assets), "without a value first when ascending")
	assert.Equal(t, []string{"checkout-api-1", "app"},
		namesOf(f.list(t, "sort=memory&order=desc&limit=2").Assets))
	assert.Equal(t, "checkout-api-1", f.list(t, "sort=restarts&order=desc&limit=1").Assets[0].Name)

	assert.Equal(t, []string{"checkout-api-1", "checkout-api-2", "old-job"},
		namesOf(f.list(t, "kind=pod&sort=age").Assets), "youngest first")
	assert.Equal(t, []string{"old-job", "checkout-api-2", "checkout-api-1"},
		namesOf(f.list(t, "kind=pod&sort=age&order=desc").Assets))
	assert.Equal(t, []string{"checkout-api-1", "checkout-api-2", "old-job"},
		namesOf(f.list(t, "kind=pod&sort=lastSeen").Assets), "most recently seen first")

	assert.Equal(t, []string{"checkout-api-1", "checkout-api", "app", "shop"},
		namesOf(f.list(t, "sort=attention&health=critical,warning&state=new,active,not_reporting").Assets),
		"worst first, leaves before the parents repeating their reason")

	test_utils.MakeGetRequest(t, f.router, "/api/v1/inventory/assets?sort=bogus", f.auth, http.StatusBadRequest)
	test_utils.MakeGetRequest(t, f.router, "/api/v1/inventory/assets?order=up", f.auth, http.StatusBadRequest)
}

func Test_ListAssets_WithFiltersAndSearch_ReturnsOnlyMatchesAndTheirCount(t *testing.T) {
	f := newPagingFixture(t)

	cases := []struct {
		query string
		want  []string
	}{
		{"kind=pod&health=critical,healthy", []string{"checkout-api-1", "checkout-api-2"}},
		{"state=terminated", []string{"old-job"}},
		{"cluster=prd&kind=cluster,node", []string{"ip-10-0-1-5", "prd"}},
		{"namespace=shop&kind=pod,container", []string{"checkout-api-1", "app", "checkout-api-2", "old-job"}},
		{"projectId=" + f.other.String(), []string{"build-01"}},
		{"q=uid-1", []string{"checkout-api-1"}},
		{"q=k8s_pod_uid", nil},
		{"q=CONTOSO", []string{"build-01"}},
		{"q=amazon linux", []string{"build-01"}},
		{"q=ec2.internal&kind=pod", []string{"checkout-api-1", "checkout-api-2"}},
		{"q=" + url.QueryEscape("%"), nil},
		{"projectId=" + f.project.String() + "&parent=" + url.QueryEscape("workload:prd/shop/checkout-api"),
			[]string{"checkout-api-1", "checkout-api-2"}},
		{"roots=true", []string{"build-01", "prd", "old-job"}},
	}
	for _, c := range cases {
		list := f.list(t, c.query)
		assert.Equal(t, c.want, namesOrNil(list.Assets), c.query)
		assert.Equal(t, int64(len(c.want)), list.Total, c.query)
	}

	limited := f.list(t, "namespace=shop&limit=2")
	assert.Len(t, limited.Assets, 2)
	assert.Equal(t, int64(6), limited.Total, "the count of the filtered matches")
}

func Test_ListAssets_WithChildCountsAndAncestors_ReturnsTheTreeAroundTheMatches(t *testing.T) {
	f := newPagingFixture(t)

	roots := f.list(t, "roots=true&childCounts=true")
	counts := map[string]int64{}
	for _, a := range roots.Assets {
		require.NotNil(t, a.ChildCount, a.Name)
		counts[a.Name] = *a.ChildCount
	}
	assert.Equal(t, map[string]int64{"prd": 2, "build-01": 0, "old-job": 0}, counts)

	matches := f.list(t, "q=app&kind=container&ancestors=true")
	assert.Equal(t, []string{"app"}, namesOf(matches.Assets))
	assert.ElementsMatch(t, []string{"checkout-api-1", "checkout-api", "shop", "prd"}, namesOf(matches.Ancestors),
		"every parent up to the cluster")
	assert.Equal(t, "Inventory paging", matches.Ancestors[0].ProjectName)

	pods := f.list(t, "kind=pod,workload&health=critical&ancestors=true")
	assert.Equal(t, []string{"checkout-api", "checkout-api-1"}, namesOf(pods.Assets))
	assert.ElementsMatch(t, []string{"shop", "prd"}, namesOf(pods.Ancestors),
		"a parent that is itself a match isn't repeated")

	orphan := f.list(t, "state=terminated&ancestors=true")
	assert.Empty(t, orphan.Ancestors, "a parent no longer in the inventory is skipped")
}

func Test_InventorySummaryAndChanges_ReturnLiveCountsRegionsClustersAndAssetIDs(t *testing.T) {
	f := newPagingFixture(t)

	var summary SummaryDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/inventory/summary", f.auth, http.StatusOK, &summary)
	assert.Equal(t, int64(9), summary.Total)
	assert.Equal(t, map[string]int64{"critical": 2, "warning": 2, "healthy": 4}, summary.Live,
		"terminated assets aren't live")
	assert.Equal(t, []string{"prd"}, summary.Clusters)
	assert.Equal(t, []RegionCountDTO{
		{Provider: "aws", Region: "us-east-1", Assets: 2, Projects: []string{"Inventory paging"}},
		{Provider: "", Region: "eu-west-1", Assets: 1, Projects: []string{"Contoso shop"}},
	}, summary.Regions)
	live := map[string]map[string]int64{}
	for _, p := range summary.Projects {
		live[p.Name] = p.Live
	}
	assert.Equal(t, map[string]int64{"healthy": 1}, live["Contoso shop"])
	assert.Equal(t, int64(2), live["Inventory paging"]["critical"])

	var changes []ChangeDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/inventory/changes", f.auth, http.StatusOK, &changes)
	require.Len(t, changes, 2)
	byKey := map[string]ChangeDTO{}
	for _, c := range changes {
		byKey[c.AssetKey] = c
	}
	pod := f.list(t, "q=checkout-api-1&kind=pod").Assets[0]
	require.NotNil(t, byKey[pod.Key].AssetID)
	assert.Equal(t, pod.ID, *byKey[pod.Key].AssetID, "a change links to its asset")
	assert.Nil(t, byKey["pod:prd/shop/uid-gone"].AssetID, "an asset no longer in the inventory has no id")
}

func newPagingFixture(t *testing.T) *pagingFixture {
	t.Helper()
	testService := &Service{repo, func() time.Time { return testNow }}
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), &Controller{testService})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Inventory paging", owner.Token, router)
	other, _ := projects_testing.CreateTestProjectWithToken("Contoso shop", owner.Token, router)

	float := func(v float64) *float64 { return &v }
	asset := func(kind, key, name, parent, health string, age time.Duration) *Asset {
		return &Asset{
			ProjectID: project.ID, Kind: kind, Key: key, Name: name, ParentKey: parent, Cluster: "prd",
			Health: health, State: StateActive, FirstSeen: testNow.Add(-age), LastSeen: testNow.Add(-age / 60),
			Attributes: Attributes{},
		}
	}
	aws := Attributes{"cloud_provider": "aws", "cloud_region": "us-east-1"}
	cluster := asset(KindCluster, "cluster:prd", "prd", "", HealthHealthy, 72*time.Hour)
	cluster.Attributes = aws
	namespace := asset(KindNamespace, "namespace:prd/shop", "shop", cluster.Key, HealthWarning, 70*time.Hour)
	workload := asset(KindWorkload, "workload:prd/shop/checkout-api", "checkout-api", namespace.Key,
		HealthCritical, 48*time.Hour)
	pod1 := asset(KindPod, "pod:prd/shop/uid-1", "checkout-api-1", workload.Key, HealthCritical, time.Hour)
	pod1.Attributes = Attributes{"k8s_pod_uid": "uid-1"}
	pod1.Metrics = Metrics{CPUPercent: float(90), MemoryPercent: float(40), Restarts24h: float(5)}
	pod2 := asset(KindPod, "pod:prd/shop/uid-2", "checkout-api-2", workload.Key, HealthHealthy, 2*time.Hour)
	pod2.Metrics = Metrics{CPUPercent: float(10)}
	container := asset(KindContainer, "container:prd/shop/uid-1/app", "app", pod1.Key, HealthWarning, time.Hour)
	container.Metrics = Metrics{CPUPercent: float(50), MemoryPercent: float(20)}
	node := asset(KindNode, "node:prd/ip-10-0-1-5", "ip-10-0-1-5", cluster.Key, HealthHealthy, 72*time.Hour)
	node.Attributes = aws
	node.Metrics = Metrics{CPUPercent: float(30)}
	orphan := asset(KindPod, "pod:prd/shop/uid-old", "old-job", "workload:prd/shop/gone", HealthInactive,
		100*time.Hour)
	orphan.State = StateTerminated
	for _, a := range []*Asset{namespace, workload, pod1, pod2, container, orphan} {
		a.Namespace = "shop"
	}
	for _, a := range []*Asset{pod1, pod2, container} {
		a.Node = "ip-10-0-1-5.ec2.internal"
	}
	host := &Asset{
		ProjectID: other.ID, Kind: KindHost, Key: "host:build-01", Name: "build-01", Health: HealthHealthy,
		State: StateActive, FirstSeen: testNow.Add(-24 * time.Hour), LastSeen: testNow.Add(-time.Minute),
		Attributes: Attributes{"os_description": "Amazon Linux 2023", "cloud_region": "eu-west-1"},
	}
	changes := []Change{
		{ProjectID: project.ID, AssetKey: pod1.Key, AssetName: pod1.Name, Kind: KindPod, Type: ChangeNew,
			At: testNow.Add(-time.Hour)},
		{ProjectID: project.ID, AssetKey: "pod:prd/shop/uid-gone", AssetName: "gone", Kind: KindPod,
			Type: ChangeNew, At: testNow.Add(-2 * time.Hour)},
	}
	require.NoError(t, repo.save(context.Background(),
		[]*Asset{cluster, namespace, workload, pod1, pod2, container, node, orphan, host}, nil, changes, nil))
	return &pagingFixture{router, "Bearer " + owner.Token, project.ID, other.ID}
}

func (f *pagingFixture) list(t *testing.T, query string) AssetListDTO {
	t.Helper()
	var list AssetListDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/inventory/assets?"+query, f.auth, http.StatusOK,
		&list)
	return list
}

func namesOf(assets []AssetDTO) []string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = a.Name
	}
	return names
}

func namesOrNil(assets []AssetDTO) []string {
	if len(assets) == 0 {
		return nil
	}
	return namesOf(assets)
}
