package spans_compactor_test

import (
	"database/sql"
	"fmt"
	"math/rand"
	"testing"
	"time"

	spans_compactor "obzly/internal/features/spans/compactor"
	spans_core "obzly/internal/features/spans/core"
	spans_ingester "obzly/internal/features/spans/ingester"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

const fanOut = 6

// Traces whose spans land in different flush files are tail sampled whole
// when their day merges cold: error and slow traces always, the rest at
// about the rate. Late spans for the same day get the same decision.
func Test_TailSampling_FanOut_KeepsTracesWholeOrDropsThem(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3).Add(9 * time.Hour)
	random := rand.New(rand.NewSource(11))
	const traceCount = 500
	traces := make([]string, traceCount)
	kind := make(map[string]string, traceCount)
	for i := range traces {
		traces[i] = fmt.Sprintf("%016x%016x", random.Uint64(), random.Uint64())
		switch {
		case i%10 == 0:
			kind[traces[i]] = "error"
		case i%10 == 1:
			kind[traces[i]] = "slow"
		default:
			kind[traces[i]] = "plain"
		}
	}
	ingester, err := spans_ingester.NewSpanIngester(dataDir, segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	// Each service's spans flush on their own: a trace spans 6 files.
	for service := range fanOut {
		var batch []*spans_core.Span
		for i, traceID := range traces {
			start := day.Add(time.Duration(i) * time.Second)
			span := &spans_core.Span{
				ID: uuid.New(), ProjectID: projectID, TraceID: traceID,
				SpanID: fmt.Sprintf("%016x", random.Uint64()), Name: "op",
				ServiceName: fmt.Sprintf("svc-%d", service),
				StartTime:   start, EndTime: start.Add(5 * time.Millisecond), StatusCode: spans_core.StatusCodeOK,
			}
			if service == 3 && kind[traceID] == "error" {
				span.StatusCode = spans_core.StatusCodeError
			}
			if service == 4 && kind[traceID] == "slow" {
				span.EndTime = start.Add(2 * time.Second)
			}
			batch = append(batch, span)
		}
		require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: batch}))
		_, err := ingester.FlushAll()
		require.NoError(t, err)
	}

	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	sampling := spans_core.SpanSampling{Rate: 0.2, SlowMs: 1000}
	compactUntilCold(t, compactor, projectID, sampling)

	counts := spansPerTrace(t, projectID)
	kept := 0
	for _, traceID := range traces {
		n := counts[traceID]
		require.Contains(t, []int{0, fanOut}, n, "trace %s (%s) is whole or gone", traceID, kind[traceID])
		if kind[traceID] != "plain" {
			require.Equal(t, fanOut, n, "%s traces are always kept", kind[traceID])
		} else if n > 0 {
			kept++
		}
	}
	plain := traceCount * 8 / 10
	require.InDelta(t, 0.2*float64(plain), kept, 0.08*float64(plain), "plain traces kept at about the rate")

	// Late spans for every trace merge into the same day afterwards and
	// follow the decision already made.
	var late []*spans_core.Span
	for i, traceID := range traces {
		start := day.Add(time.Duration(i)*time.Second + time.Second/2)
		late = append(late, &spans_core.Span{
			ID: uuid.New(), ProjectID: projectID, TraceID: traceID, SpanID: fmt.Sprintf("%016x", random.Uint64()),
			Name: "late", ServiceName: "svc-late", StartTime: start, EndTime: start.Add(time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		})
	}
	require.NoError(t, ingester.StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{projectID: late}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	compactUntilCold(t, compactor, projectID, sampling)

	after := spansPerTrace(t, projectID)
	for _, traceID := range traces {
		if counts[traceID] == 0 {
			require.Zero(t, after[traceID], "trace %s stays dropped", traceID)
		} else {
			require.Equal(t, fanOut+1, after[traceID], "trace %s keeps its late span", traceID)
		}
	}
}

// Without sampling (rate 1) every span stays.
func Test_TailSampling_RateOne_KeepsEverySpan(t *testing.T) {
	dataDir := t.TempDir()
	projectID := uuid.New()
	seedTinySegments(t, dataDir, projectID, 20, time.Now().UTC().AddDate(0, 0, -3))
	compactor, err := spans_compactor.NewSpanCompactor(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	compactUntilCold(t, compactor, projectID, spans_core.SpanSampling{Rate: 1})
	total := 0
	for _, n := range spansPerTrace(t, projectID) {
		total += n
	}
	require.Equal(t, 20, total)
}

// compactUntilCold runs compaction passes, with L0 files aged past
// promotion, until every file is cold (the day is over).
func compactUntilCold(
	t *testing.T, compactor *spans_compactor.SpanCompactor, projectID uuid.UUID, sampling spans_core.SpanSampling,
) {
	t.Helper()
	allCold := func() bool {
		records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "spans")
		require.NoError(t, err)
		for _, record := range records {
			if record.Level != segments.LevelCold {
				return false
			}
		}
		return true
	}
	for range 6 {
		test_utils.AgeSegments(t, projectID, "spans", 48*time.Hour)
		_, err := compactor.CompactSmallSegments(projectID, sampling)
		require.NoError(t, err)
		if allCold() {
			return
		}
	}
	require.True(t, allCold(), "everything ends cold")
}

// spansPerTrace counts the project's spans per trace in its segment files.
func spansPerTrace(t *testing.T, projectID uuid.UUID) map[string]int {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "spans")
	require.NoError(t, err)
	counts := map[string]int{}
	if len(records) == 0 {
		return counts
	}
	paths := make([]string, len(records))
	for i, record := range records {
		paths[i] = record.Path
	}
	from, args := segments.ParquetFilesExpr(paths)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT trace_id, count(*) FROM "+from+" GROUP BY 1", args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var traceID string
		var n int
		require.NoError(t, rows.Scan(&traceID, &n))
		counts[traceID] = n
	}
	return counts
}
