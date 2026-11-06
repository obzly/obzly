package segments_test

import (
	"fmt"
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func logsStatsSchema() segments.Schema {
	return segments.Schema{
		Stream: "logs",
		Columns: []segments.ColumnDef{
			{Name: "id", Type: "VARCHAR"},
			{Name: "service_name", Type: "VARCHAR"},
			{Name: "level", Type: "VARCHAR"},
			{Name: "timestamp", Type: "TIMESTAMP_NS"},
		},
	}
}

func Test_WriterFlush_RecordsDistinctServicesAndLowercaseLevels(t *testing.T) {
	now := time.Now()
	records := flushSegments(t, logsStatsSchema(), [][]any{
		{"1", "svc-b", "ERROR", now},
		{"2", "svc-a", "info", now},
		{"3", "svc-b", "info", now},
	})

	stats := records[0].Stats
	require.NotNil(t, stats)
	require.Equal(t, []string{"svc-a", "svc-b"}, stats.Services)
	require.Equal(t, []string{"error", "info"}, stats.Levels)
	require.Nil(t, stats.MetricNames)
}

func Test_WriterFlush_StreamWithoutStats_RecordsNone(t *testing.T) {
	records := flushSegments(t, testSchema(), [][]any{{"a", time.Now(), int64(1)}})

	require.Nil(t, records[0].Stats)
}

func Test_WriterFlush_TooManyDistinctValues_RecordsListAsUnknown(t *testing.T) {
	now := time.Now()
	rows := make([][]any, 0, 300)
	for i := 0; i < 300; i++ {
		rows = append(rows, []any{fmt.Sprint(i), fmt.Sprintf("svc-%03d", i), "info", now})
	}

	stats := flushSegments(t, logsStatsSchema(), rows)[0].Stats

	require.Nil(t, stats.Services, "over the cap: unknown, so never pruned")
	require.Equal(t, []string{"info"}, stats.Levels)
}

func Test_MergeSegments_UnionsInputStats(t *testing.T) {
	now := time.Now()
	records := flushSegments(t, logsStatsSchema(),
		[][]any{{"1", "svc-a", "info", now}},
		[][]any{{"2", "svc-b", "warn", now}},
	)

	merged := mergeSegments(t, records)

	require.Equal(t, []string{"svc-a", "svc-b"}, merged.Stats.Services)
	require.Equal(t, []string{"info", "warn"}, merged.Stats.Levels)
}

func Test_MergeSegments_InputWithoutStats_MakesMergedStatsUnknown(t *testing.T) {
	now := time.Now()
	records := flushSegments(t, logsStatsSchema(),
		[][]any{{"1", "svc-a", "info", now}},
		[][]any{{"2", "svc-b", "info", now}},
	)
	records[1].Stats = nil // written before stats existed

	merged := mergeSegments(t, records)

	require.Nil(t, merged.Stats.Services)
	require.Nil(t, merged.Stats.Levels)
}

func Test_SegmentStats_RoundTripThroughCatalog(t *testing.T) {
	repo := &segments.SegmentRepository{}
	stream := uniqueStream()
	projectID := uuid.New()
	base := time.Now().UTC()

	withStats := segments.FromSegment(sampleSegment(projectID, stream, base, base))
	withStats.Stats = &segments.SegmentStats{Services: []string{"svc-a"}, Levels: []string{}}
	require.NoError(t, repo.Create(withStats))
	require.NoError(t, repo.Create(segments.FromSegment(sampleSegment(projectID, stream, base, base))))
	t.Cleanup(func() { _, _ = repo.DeleteByProject(projectID, stream) })

	records, err := repo.ListByTimeRange(projectID, stream, base.Add(-time.Minute), base.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, records, 2)

	byID := map[uuid.UUID]*segments.SegmentRecord{records[0].ID: records[0], records[1].ID: records[1]}
	require.Equal(t, &segments.SegmentStats{Services: []string{"svc-a"}, Levels: []string{}},
		byID[withStats.ID].Stats, "an empty list stays known-empty, distinct from unknown")
	for _, record := range records {
		if record.ID != withStats.ID {
			require.Nil(t, record.Stats)
		}
	}
}
