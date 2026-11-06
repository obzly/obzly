package segments_test

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var planNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func Test_PlanCompaction_YoungSmallFlushes_WaitForMore(t *testing.T) {
	plan := segments.PlanCompaction(flushes(3, 2<<20, time.Hour), planNow)

	require.Empty(t, plan.Merges)
	require.Empty(t, plan.Promote)
}

func Test_PlanCompaction_L0MaxFilesFlushes_MergeIntoL1(t *testing.T) {
	plan := segments.PlanCompaction(flushes(segments.L0MaxFiles, 1<<20, time.Hour), planNow)

	require.Len(t, plan.Merges, 1)
	require.Len(t, plan.Merges[0].Records, segments.L0MaxFiles)
	require.Equal(t, segments.LevelCompacted, plan.Merges[0].Level)
}

func Test_PlanCompaction_FlushesReachingL1Target_MergeWithoutWaiting(t *testing.T) {
	plan := segments.PlanCompaction(flushes(7, 20<<20, time.Hour), planNow)

	require.Len(t, plan.Merges, 1, "the first 6 (120 MB) fill an L1; the 7th waits")
	require.Len(t, plan.Merges[0].Records, 6)
}

func Test_PlanCompaction_OldFlushes_MergeOrPromoteRegardlessOfSize(t *testing.T) {
	pair := segments.PlanCompaction(flushes(2, 1<<20, 25*time.Hour), planNow)
	require.Len(t, pair.Merges, 1)

	lone := segments.PlanCompaction(flushes(1, 1<<20, 25*time.Hour), planNow)
	require.Empty(t, lone.Merges)
	require.Len(t, lone.Promote, 1, "a lone old flush is relabelled L1, not rewritten")
}

func Test_PlanCompaction_L1ReachingL2Min_MergesIntoCold(t *testing.T) {
	l1 := withLevel(flushes(5, 120<<20, 48*time.Hour), segments.LevelCompacted)

	plan := segments.PlanCompaction(l1, planNow)

	require.Len(t, plan.Merges, 1)
	require.Equal(t, segments.LevelCold, plan.Merges[0].Level)
	require.LessOrEqual(t, totalBytes(plan.Merges[0].Records), int64(segments.L2MaxBytes))

	small := segments.PlanCompaction(withLevel(flushes(3, 100<<20, 2*time.Hour), segments.LevelCompacted), planNow)
	require.Empty(t, small.Merges, "300 MB of today's L1 waits for L2MinBytes")
}

func Test_PlanCompaction_LargeRowFromBeforeLevels_CountsAsL1(t *testing.T) {
	records := flushes(segments.L0MaxFiles, 40<<20, time.Hour)

	plan := segments.PlanCompaction(records, planNow)

	require.Equal(t, segments.LevelCompacted, segments.EffectiveLevel(records[0]))
	for _, group := range plan.Merges {
		require.Equal(t, segments.LevelCold, group.Level, "never re-merged as L0")
	}
}

func Test_PlanCompaction_NeverMergesAcrossSchemaVersions(t *testing.T) {
	records := flushes(segments.L0MaxFiles*2, 1<<20, time.Hour)
	for _, record := range records[:segments.L0MaxFiles] {
		record.SchemaVersion = segments.SchemaVersionLegacy
	}

	plan := segments.PlanCompaction(records, planNow)

	require.Len(t, plan.Merges, 2)
	for _, group := range plan.Merges {
		for _, record := range group.Records {
			require.Equal(t, group.Records[0].SchemaVersion, record.SchemaVersion)
		}
	}
}

// Once a UTC day is over (plus grace for late data), its L1 files merge
// into one cold file whatever their size.
func Test_PlanCompaction_EndedDaysL1_MergesIntoOneColdFileRegardlessOfSize(t *testing.T) {
	plan := segments.PlanCompaction(withLevel(flushes(3, 10<<20, 48*time.Hour), segments.LevelCompacted), planNow)

	require.Len(t, plan.Merges, 1)
	require.Equal(t, segments.LevelCold, plan.Merges[0].Level)
	require.Len(t, plan.Merges[0].Records, 3)
}

// A quiet day's one L1 file is rewritten cold too (else it would never get
// rollups or trace sampling); a lone L1 of a day not over yet waits.
func Test_PlanCompaction_EndedDaysLoneL1_IsRewrittenCold(t *testing.T) {
	plan := segments.PlanCompaction(withLevel(flushes(1, 10<<20, 48*time.Hour), segments.LevelCompacted), planNow)
	require.Len(t, plan.Merges, 1)
	require.Equal(t, segments.LevelCold, plan.Merges[0].Level)

	today := segments.PlanCompaction(withLevel(flushes(1, 10<<20, time.Hour), segments.LevelCompacted), planNow)
	require.Empty(t, today.Merges)
}

// A cold file never spans UTC days, so a query window opens only its days.
func Test_PlanCompaction_ColdMerges_NeverCrossAUTCDay(t *testing.T) {
	var l1 []*segments.SegmentRecord
	for _, day := range []time.Time{
		time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC), time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 29, 0, 30, 0, 0, time.UTC), time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC),
	} {
		l1 = append(l1, &segments.SegmentRecord{
			ID:            uuid.New(),
			SizeBytes:     100 << 20,
			Level:         segments.LevelCompacted,
			MinTime:       day,
			MaxTime:       day.Add(30 * time.Minute),
			CreatedAt:     day,
			SchemaVersion: segments.CurrentSchemaVersion,
		})
	}

	plan := segments.PlanCompaction(l1, planNow)

	require.Len(t, plan.Merges, 2, "one per day, though 400 MB would fit one file")
	for _, group := range plan.Merges {
		require.Equal(t, group.Records[0].MinTime.Day(), group.Records[1].MinTime.Day())
	}
}

// An L1 merge sorts in memory, so its raw size is capped as well as its
// compressed size.
func Test_PlanCompaction_RunsReachingTheRawCap_MergeAtIt(t *testing.T) {
	records := flushes(5, 5<<20, time.Hour)
	for _, record := range records {
		record.RawSizeBytes = 100 << 20
	}

	plan := segments.PlanCompaction(records, planNow)

	require.Len(t, plan.Merges, 2, "2 x 100 MB raw fill the 256 MB cap; the 5th waits")
	for _, group := range plan.Merges {
		require.Len(t, group.Records, 2)
	}
}

// Right after an upgrade a project can have gigabytes of old L0: one pass
// merges at most PassBudgetBytes of it, so ingest keeps its CPU.
func Test_PlanCompaction_LargeBacklog_MergesAtMostThePassBudget(t *testing.T) {
	plan := segments.PlanCompaction(flushes(30, 20<<20, 48*time.Hour), planNow)

	var merged int64
	for _, group := range plan.Merges {
		merged += totalBytes(group.Records)
	}
	require.Len(t, plan.Merges, 4, "four 120 MB runs; the fifth waits for the next pass")
	require.LessOrEqual(t, merged, int64(segments.PassBudgetBytes))
}

// flushes returns n time-ordered L0 records of size bytes, created age ago.
func flushes(n int, size int64, age time.Duration) []*segments.SegmentRecord {
	records := make([]*segments.SegmentRecord, n)
	for i := range records {
		start := planNow.Add(-age).Add(time.Duration(i) * time.Minute)
		records[i] = &segments.SegmentRecord{
			ID: uuid.New(), SizeBytes: size, MinTime: start, MaxTime: start.Add(time.Minute),
			CreatedAt: start, SchemaVersion: segments.CurrentSchemaVersion,
		}
	}
	return records
}

func withLevel(records []*segments.SegmentRecord, level int) []*segments.SegmentRecord {
	for _, record := range records {
		record.Level = level
	}
	return records
}

func totalBytes(records []*segments.SegmentRecord) int64 {
	var total int64
	for _, record := range records {
		total += record.SizeBytes
	}
	return total
}
