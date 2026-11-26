package logs_querier_test

import (
	"context"
	"sort"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hourlyFixture: one compacted segment per hour, each holding its own
// messages, so a test can tell which segments a query opened.
type hourlyFixture struct {
	projectID uuid.UUID
	querier   *logs_querier.LogQuerier
	compactor *logs_compactor.LogCompactor
	segments  []*segments.SegmentRecord
	base      time.Time
}

// A window ending at a whole minute has a zero-width edge there, counted raw:
// only the segment holding that instant is opened for it, not every segment
// of the window (Volume and Levels end at now and used to open them all).
func Test_GroupWindows_WindowEndingInTheNewestSegment_OpensNoOtherRawSegment(t *testing.T) {
	f := seedHourlyFixture(t, [][]string{
		{"cache flushed", "cache flushed"},
		{"user 7 signed in"},
		{"payment failed for order 9", "payment failed for order 10"},
	})
	end := f.base.Add(2*time.Hour + 30*time.Minute) // the last segment's own minute
	request := func() *logs_core.GroupWindowsRequestDTO {
		return &logs_core.GroupWindowsRequestDTO{
			GroupField: "service_name",
			Windows:    []logs_core.CountWindowDTO{{From: f.base, To: end}},
			Selections: []logs_core.GroupSelectionDTO{{OrderBy: "w0 DESC", Limit: 10}},
		}
	}
	want, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, request())
	require.NoError(t, err)
	require.Equal(t, int64(4), want.Totals[0], "the line at the window's last instant counts, the one after it not")

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	before := rawSegmentReads()
	got, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, request())
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1.0, rawSegmentReads()-before, "only the newest segment, for the edge")
}

// Each pattern's sample is read from its newest hour, which the rollups
// locate: the segment between them, holding none of the wanted patterns, is
// never opened (newest-first windows used to read through it to reach the
// older pattern).
func Test_PatternSamples_WithRollups_ReadOnlyEachPatternsNewestHour(t *testing.T) {
	f := seedHourlyFixture(t, [][]string{
		{"cache flushed", "user 3 signed in"},
		{"request 1 took 2ms", "request 5 took 9ms"},
		{"payment failed for order 9", "user 4 signed in"},
	})
	signedIn := logs_core.PatternHash("user 3 signed in")
	hashes := []string{logs_core.PatternHash("cache flushed"), logs_core.PatternHash("payment failed for order 9"),
		signedIn, logs_core.PatternHash("no such line")}
	samples := func() map[string]logs_core.PatternSampleDTO {
		found, err := f.querier.ExecutePatternSamples(context.Background(), f.projectID,
			&logs_core.PatternSamplesRequestDTO{
				TimeRange: &logs_core.TimeRangeDTO{From: new(f.base), To: new(f.base.Add(3 * time.Hour))},
				Hashes:    hashes,
			})
		require.NoError(t, err)
		return found
	}
	want := samples()
	require.Len(t, want, 3)
	assert.Equal(t, "user 4 signed in", want[signedIn].Sample, "the newest line of the pattern")

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	before := rawSegmentReads()
	assert.Equal(t, want, samples())
	assert.Equal(t, 2.0, rawSegmentReads()-before, "the first and the last hour's segments")
}

// seedHourlyFixture stores messages[i] in hour i after base, one compacted
// segment per hour, each line at the hour's 30th minute (plus a second).
func seedHourlyFixture(t *testing.T, messages [][]string) hourlyFixture {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	for hour, lines := range messages {
		var items []*logs_core.LogItem
		for i, message := range lines {
			items = append(items, &logs_core.LogItem{
				ID: uuid.New(), ProjectID: projectID, Level: logs_core.LogLevelInfo, Message: message,
				Timestamp: base.Add(time.Duration(hour)*time.Hour + 30*time.Minute + time.Duration(i)*time.Second),
				Fields:    map[string]any{"service_name": "shop"},
			})
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.Len(t, records, len(messages))
	sort.Slice(records, func(i, j int) bool { return records[i].MinTime.Before(records[j].MinTime) })
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCompacted))

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	return hourlyFixture{projectID, querier, compactor, records, base}
}

// rawSegmentReads is how many raw log segments queries opened so far.
func rawSegmentReads() float64 {
	var total float64
	for _, source := range []string{telemetry.SourceLocal, telemetry.SourceCache, telemetry.SourceObjectStore,
		telemetry.SourcePartial, telemetry.SourceMissing, telemetry.SourcePending} {
		total += testutil.ToFloat64(telemetry.QuerySegmentReads.WithLabelValues(logs_core.StreamLogs, source))
	}
	return total
}
