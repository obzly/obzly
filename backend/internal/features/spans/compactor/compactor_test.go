package spans_compactor_test

import (
	"testing"
	"time"

	spans_compactor "obzly/internal/features/spans/compactor"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// seedTinySegments ingests one span at a time with maxRows(1), so each
// flushes to its own segment file.
func seedTinySegments(t *testing.T, dataDir string, projectID uuid.UUID, count int, startTime time.Time) {
	t.Helper()

	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithMaxRows(1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	for i := range count {
		start := startTime.Add(time.Duration(i) * time.Minute)
		span := &spans_core.Span{
			ID: uuid.New(), ProjectID: projectID,
			TraceID: uuid.NewString(), SpanID: uuid.NewString()[:16],
			Name: "op", ServiceName: "svc",
			StartTime: start, EndTime: start.Add(time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		}
		require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: {span}}))
	}
}

func Test_EnforceRetention_DeletesSegmentsOlderThanCutoff(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	startTime := time.Now().UTC().Add(-48 * time.Hour)

	seedTinySegments(t, dataDir, projectID, 10, startTime) // spans 48h ago -> ~38h ago

	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.EnforceRetention(projectID, 1) // keep only last 1 day
	require.NoError(t, err)
	require.Equal(t, 10, deleted, "all 10 segments are older than 1 day")

	segmentRepo := &segments.SegmentRepository{}
	remaining, err := segmentRepo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func Test_EnforceRetention_ZeroMaxLifeDays_IsNoOp(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 3, time.Now().UTC().Add(-100*24*time.Hour))

	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.EnforceRetention(projectID, 0)
	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func Test_DeleteAllForProject_RemovesEverySegmentRegardlessOfAge(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 5, time.Now().UTC())

	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	deleted, err := compactor.DeleteAllForProject(projectID)
	require.NoError(t, err)
	require.Equal(t, 5, deleted)

	segmentRepo := &segments.SegmentRepository{}
	remaining, err := segmentRepo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func Test_CompactSmallSegments_MergesConsecutiveSmallSegmentsIntoOne(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 4, time.Now().UTC())

	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	test_utils.AgeSegments(t, projectID, "spans", 48*time.Hour)
	result, err := compactor.CompactSmallSegments(projectID, spans_core.SpanSampling{Rate: 1})
	require.NoError(t, err)
	require.Equal(t, 1, result.MergedGroups)
	require.Equal(t, 4, result.SegmentsRemoved)
	require.Equal(t, 1, result.SegmentsCreated)

	segmentRepo := &segments.SegmentRepository{}
	remaining, err := segmentRepo.ListByProject(projectID, "spans")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.EqualValues(t, 4, remaining[0].RowCount)
}
