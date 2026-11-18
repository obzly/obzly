package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingCollector counts collects and fails each, so a build writes nothing
// to the shared test database's other projects.
type countingCollector struct {
	calls atomic.Int64
}

func Test_BuildIfDue_TwoWorkersOneValkey_BuildOncePerInterval(t *testing.T) {
	projectID := uuid.New()
	require.NoError(t, storage.GetDb().Exec("INSERT INTO projects (id, name) VALUES (?, ?)",
		projectID, "inventory interval "+projectID.String()[:8]).Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM projects WHERE id = ?", projectID) })
	previousEvery, previousTask := buildEvery, buildTask
	buildEvery, buildTask = time.Second, "inventory-build-test-"+uuid.NewString()
	t.Cleanup(func() { buildEvery, buildTask = previousEvery, previousTask })

	collectorA, collectorB := &countingCollector{}, &countingCollector{}
	workerA := &Builder{repo, collectorA, slog.Default(), time.Now}
	workerB := &Builder{repo, collectorB, slog.Default(), time.Now}
	tick := func() int {
		built := 0
		for _, worker := range []*Builder{workerA, workerB} {
			ran, err := worker.buildIfDue(context.Background())
			require.NoError(t, err)
			if ran {
				built++
			}
		}
		return built
	}

	require.Equal(t, 1, tick(), "one of the two workers builds")
	require.Equal(t, 0, tick(), "both skip within the interval")
	require.Positive(t, collectorA.calls.Load()+collectorB.calls.Load())
	require.Zero(t, min(collectorA.calls.Load(), collectorB.calls.Load()), "only one worker collected")

	time.Sleep(buildEvery)
	require.Equal(t, 1, tick(), "the next interval builds once again")
}

func Test_Build_SteadyInventory_OnlyMovesLastSeen(t *testing.T) {
	projectID := uuid.New()
	require.NoError(t, storage.GetDb().Exec("INSERT INTO projects (id, name) VALUES (?, ?)",
		projectID, "inventory steady "+projectID.String()[:8]).Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM projects WHERE id = ?", projectID) })
	now := testNow
	fake := &fakeCollector{snapshots: map[uuid.UUID]*snapshot{projectID: clusterSnapshot()}}
	testBuilder := &Builder{repo, fake, slog.Default(), func() time.Time { return now }}
	require.NoError(t, testBuilder.Build(context.Background(), projectID))

	// A minute later the same series report again, a minute newer.
	now = testNow.Add(time.Minute)
	later := clusterSnapshot()
	for i := range later.series {
		later.series[i].LastSeen = later.series[i].LastSeen.Add(time.Minute)
	}
	fake.snapshots[projectID] = later
	require.NoError(t, testBuilder.Build(context.Background(), projectID))

	pod, err := repo.assetByKey(projectID, "pod:prd/shop/uid-checkout-1")
	require.NoError(t, err)
	assert.True(t, pod.UpdatedAt.Equal(testNow), "an unchanged asset is not rewritten")
	assert.True(t, pod.LastSeen.Equal(testNow), "but its last_seen moves")
}

func Test_CapAssets_OverTheLimit_KeepsTheMostRecentlySeen(t *testing.T) {
	var assets []*Asset
	var changes []Change
	storedOld := uuid.New()
	for i := range 5 {
		a := &Asset{Key: fmt.Sprintf("host:h%d", i), LastSeen: testNow.Add(-time.Duration(i) * time.Minute)}
		if i == 4 {
			a.ID = storedOld
		}
		assets = append(assets, a)
		changes = append(changes, Change{AssetKey: a.Key})
	}

	kept, keptChanges, stale := capAssets(assets, changes, nil, 3)

	var keys []string
	for _, a := range kept {
		keys = append(keys, a.Key)
	}
	assert.Equal(t, []string{"host:h0", "host:h1", "host:h2"}, keys)
	assert.Len(t, keptChanges, 3)
	assert.Equal(t, []uuid.UUID{storedOld}, stale, "a dropped asset already stored is deleted")
}

func Test_PodRestarts_ComeFromTheContainerQuery(t *testing.T) {
	snap := clusterSnapshot()
	checkout := map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "shop", "k8s_pod_name": "checkout-api-7d9f8b6c5d-x7k2p",
		"k8s_pod_uid": "uid-checkout-1", "host_name": "ip-10-0-1-5.ec2.internal",
	}
	delete(snap.values, "pod_restarts_1h")
	snap.values["container_restarts_1h"] = []sample{
		{with(checkout, "k8s_container_name", "app"), 3},
		{with(checkout, "k8s_container_name", "sidecar"), 1},
	}

	addPodValuesFromContainers(snap.values)
	assets := observe(snap)

	pod := assets["pod:prd/shop/uid-checkout-1"]
	require.NotNil(t, pod)
	require.NotNil(t, pod.Metrics.Restarts1h)
	assert.Equal(t, 4.0, *pod.Metrics.Restarts1h, "the pod's restarts are its containers' summed")
}

func Test_ValueCache_ServesAnHourThenExpires(t *testing.T) {
	cache := &valueCache{}
	projectID := uuid.New()
	cache.put(projectID, "container_restarts_24h", testNow, []sample{{map[string]string{}, 1}})

	_, ok := cache.get(projectID, "container_restarts_24h", testNow.Add(59*time.Minute))
	assert.True(t, ok)
	_, ok = cache.get(projectID, "container_restarts_24h", testNow.Add(time.Hour))
	assert.False(t, ok)
	_, ok = cache.get(uuid.New(), "container_restarts_24h", testNow)
	assert.False(t, ok, "per project")
}

func (c *countingCollector) collect(context.Context, uuid.UUID, time.Time) (*snapshot, error) {
	c.calls.Add(1)
	return nil, errors.New("counting only")
}
