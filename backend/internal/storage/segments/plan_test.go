package segments_test

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type planFixture struct {
	projectID                                         uuid.UUID
	from, to                                          time.Time
	svcA, svcB, noStats, unknownServices, legacy, old *segments.SegmentRecord
}

func Test_PlanSegments_ServiceHint_KeepsMatchingUnknownAndLegacySegments(t *testing.T) {
	f := seedPlanFixture(t)
	before := prunedLogs()

	planned, err := planWith(f, segments.PruningHints{Services: []string{"svc-a", "svc-q"}})

	require.NoError(t, err)
	require.Equal(t, ids(f.svcA, f.noStats, f.unknownServices, f.legacy), ids(planned...))
	require.Equal(t, before+1, prunedLogs(), "only svc-b's segment is ruled out; the out-of-window one is not counted")
}

func Test_PlanSegments_HintsOnTwoFields_SegmentMustAllowBoth(t *testing.T) {
	f := seedPlanFixture(t)
	before := prunedLogs()

	planned, err := planWith(f, segments.PruningHints{Services: []string{"svc-a"}, Levels: []string{"error"}})

	require.NoError(t, err)
	require.Equal(t, ids(f.svcA, f.noStats, f.legacy), ids(planned...),
		"unknown services, but its known levels rule it out")
	require.Equal(t, before+2, prunedLogs())
}

func Test_PlanSegments_NoHints_ReturnsEverySegmentInTheWindowOldestFirst(t *testing.T) {
	f := seedPlanFixture(t)

	planned, err := planWith(f, segments.PruningHints{})

	require.NoError(t, err)
	require.Equal(t, ids(f.svcA, f.svcB, f.noStats, f.unknownServices, f.legacy), ids(planned...))
}

func Test_PlanSegments_ContradictoryHints_ReturnsNothing(t *testing.T) {
	f := seedPlanFixture(t)

	planned, err := planWith(f, segments.PruningHints{Services: []string{}})

	require.NoError(t, err)
	require.Empty(t, planned)
}

func Test_PlanSegments_EverySegmentPruned_StillCountsThem(t *testing.T) {
	repo := &segments.SegmentRepository{}
	projectID := uuid.New()
	t.Cleanup(func() { _, _ = repo.DeleteByProject(projectID, "logs") })
	now := time.Now().UTC()
	record := segments.FromSegment(sampleSegment(projectID, "logs", now, now))
	record.Stats = &segments.SegmentStats{Services: []string{"svc-b"}}
	require.NoError(t, repo.Create(record))
	before := prunedLogs()

	planned, err := repo.PlanSegments(projectID, "logs", now.Add(-time.Minute), now.Add(time.Minute),
		segments.PruningHints{Services: []string{"svc-q"}})

	require.NoError(t, err)
	require.Empty(t, planned)
	require.Equal(t, before+1, prunedLogs())
}

func Test_Intersect_NilIsNoConstraint(t *testing.T) {
	require.Nil(t, segments.Intersect(nil, nil))
	require.Equal(t, []string{"a"}, segments.Intersect(nil, []string{"a"}))
	require.Equal(t, []string{"a", "b"}, segments.Intersect([]string{"a", "b"}, nil))
	require.Equal(t, []string{"b"}, segments.Intersect([]string{"a", "b"}, []string{"b", "c"}))
	require.Equal(t, []string{}, segments.Intersect([]string{"a"}, []string{"b"}), "empty: nothing can match")
}

func seedPlanFixture(t *testing.T) planFixture {
	t.Helper()
	repo := &segments.SegmentRepository{}
	f := planFixture{projectID: uuid.New()}
	t.Cleanup(func() { _, _ = repo.DeleteByProject(f.projectID, "logs") })
	base := time.Now().UTC().Truncate(time.Second)
	f.from, f.to = base.Add(-time.Hour), base

	create := func(offset time.Duration, stats *segments.SegmentStats, schemaVersion int) *segments.SegmentRecord {
		at := base.Add(offset)
		record := segments.FromSegment(sampleSegment(f.projectID, "logs", at, at))
		record.Stats = stats
		if schemaVersion != 0 {
			record.SchemaVersion = schemaVersion
		}
		require.NoError(t, repo.Create(record))
		return record
	}
	levels := func(values ...string) []string { return values }
	f.svcA = create(-50*time.Minute,
		&segments.SegmentStats{Services: []string{"svc-a"}, Levels: levels("error", "info")}, 0)
	f.svcB = create(-40*time.Minute, &segments.SegmentStats{Services: []string{"svc-b"}, Levels: levels("info")}, 0)
	f.noStats = create(-30*time.Minute, nil, 0)
	f.unknownServices = create(-20*time.Minute, &segments.SegmentStats{Levels: levels("info")}, 0)
	f.legacy = create(-10*time.Minute, &segments.SegmentStats{Services: []string{"svc.z"}},
		segments.SchemaVersionLegacy)
	f.old = create(-3*time.Hour, &segments.SegmentStats{Services: []string{"svc-a"}}, 0)
	return f
}

func planWith(f planFixture, hints segments.PruningHints) ([]*segments.SegmentRecord, error) {
	return (&segments.SegmentRepository{}).PlanSegments(f.projectID, "logs", f.from, f.to, hints)
}

func ids(records ...*segments.SegmentRecord) []uuid.UUID {
	out := make([]uuid.UUID, len(records))
	for i, record := range records {
		out[i] = record.ID
	}
	return out
}

func prunedLogs() float64 {
	return testutil.ToFloat64(telemetry.QuerySegmentsPruned.WithLabelValues("logs"))
}
