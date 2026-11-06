package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A ReplicaSet whose pods first log inside the range is a rollout; one
// logging from the start, a StatefulSet's ordinal pods and another
// namespace (filtered out) are not - from raw logs and per-minute counts
// alike.
func Test_ExecuteDeploys_NewReplicaSetInTheRange_IsARollout(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	var items []*logs_core.LogItem
	log := func(offset time.Duration, namespace, pod string) {
		items = append(items, &logs_core.LogItem{Timestamp: base.Add(offset), Level: logs_core.LogLevelInfo,
			Message: "ok", Fields: map[string]any{"k8s_namespace_name": namespace, "k8s_pod_name": pod}})
	}
	for minute := range 90 {
		at := time.Duration(minute)*time.Minute + 7*time.Second
		log(at, "shop", "api-7f9c4d8b6-abcde")
		log(at, "shop", "db-0")
		log(at, "ops", "cron-6d5f7b9c8-zzzzz")
		if minute >= 50 {
			log(at, "shop", "api-5b6c7d8e9-fghij")
			log(at+time.Second, "shop", "api-5b6c7d8e9-klmno")
		}
		if minute >= 60 {
			log(at, "ops", "cron-8a7b6c5d4-yyyyy")
		}
	}
	projectID, querier := seedProject(t, items)
	request := &logs_core.DeploysRequestDTO{
		Query:     condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "shop"),
		TimeRange: &logs_core.TimeRangeDTO{From: &base, To: new(base.Add(2 * time.Hour))},
	}

	want := logs_core.DeployMarkerDTO{Namespace: "shop", Workload: "api", ReplicaSet: "api-5b6c7d8e9", Pods: 2}
	check := func(precision time.Duration) {
		response, err := querier.ExecuteDeploys(context.Background(), projectID, request)
		require.NoError(t, err)
		require.Len(t, response.Markers, 1)
		marker := response.Markers[0]
		assert.WithinDuration(t, base.Add(50*time.Minute+7*time.Second), marker.Time, precision)
		marker.Time = time.Time{}
		assert.Equal(t, want, marker)
	}
	check(0)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.NoError(t, repo.SetLevel([]uuid.UUID{records[0].ID}, segments.LevelCompacted))
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	require.NoError(t, compactor.RollupSegments(projectID))
	check(time.Minute) // the counts know the minute
}
