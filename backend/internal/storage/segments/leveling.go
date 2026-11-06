package segments

import (
	"time"
)

// Leveled compaction: L0 flushes merge into L1 files of about L1TargetBytes,
// and L1 files into cold L2 files of L2MinBytes..L2MaxBytes, so each byte is
// rewritten about twice.
const (
	LevelCold = 2

	L1TargetBytes = 128 << 20
	// L1MaxRawBytes also caps an L1 merge, which sorts in memory within
	// compactorMemoryLimit.
	L1MaxRawBytes = 256 << 20
	L2MinBytes    = 512 << 20
	L2MaxBytes    = 1 << 30
	// L0MaxFiles: this many L0 files merge even below L1TargetBytes, so a
	// query never opens more than about this many unmerged flushes.
	L0MaxFiles = 16
	// L0PromoteAge: L0 data this old goes to L1 regardless of size, so a
	// low-volume project doesn't keep tiny cold objects.
	L0PromoteAge = 24 * time.Hour
	// legacyCompactedBytes: rows from before the level column read 0, but
	// one this big was already compaction output (writers flushed smaller).
	legacyCompactedBytes = 32 << 20
	// PassBudgetBytes: one pass merges at most this much input per project,
	// so a backlog (e.g. right after an upgrade) is worked off over several
	// passes instead of starving ingest of CPU.
	PassBudgetBytes = 512 << 20
	// L2 files hold one UTC day each (by their earliest row), so a query
	// window only opens the days it covers. A day's L1 files merge once it
	// has been over for dayCloseGrace (late data), whatever their size.
	dayCloseGrace = 6 * time.Hour
)

// CompactionGroup is segments to merge into one file at Level.
type CompactionGroup struct {
	Records []*SegmentRecord
	Level   int
}

// CompactionPlan is one project's work for a pass: merges, and lone old L0
// segments relabelled L1 without a rewrite.
type CompactionPlan struct {
	Merges  []CompactionGroup
	Promote []*SegmentRecord
}

type run struct {
	records []*SegmentRecord
	bytes   int64
	raw     int64
	// closed: the next segment would have taken it past a cap, so it is as
	// full as it gets.
	closed bool
}

// EffectiveLevel is a segment's level, counting a large pre-level row as L1.
func EffectiveLevel(record *SegmentRecord) int {
	if record.Level == LevelFlush && record.SizeBytes >= legacyCompactedBytes {
		return LevelCompacted
	}
	return record.Level
}

// PlanCompaction plans a project's merges from its segments, oldest first.
// L0 runs merge (sorted) once they reach L1TargetBytes, L1MaxRawBytes or
// L0MaxFiles, or once their oldest file is L0PromoteAge old; L1 runs of one
// UTC day merge (concatenated) once they reach L2MinBytes or the day is over
// (a lone file too, then).
// Legacy (version 1) files never merge with current ones; versions 2 and 3
// do, into 3. At most PassBudgetBytes of input is merged per pass.
func PlanCompaction(records []*SegmentRecord, now time.Time) CompactionPlan {
	var plan CompactionPlan
	var l0, l1 []*SegmentRecord
	for _, record := range records {
		switch EffectiveLevel(record) {
		case LevelFlush:
			l0 = append(l0, record)
		case LevelCompacted:
			l1 = append(l1, record)
		}
	}

	for _, run := range runs(l0, L1TargetBytes, L1MaxRawBytes, false) {
		full := run.bytes >= L1TargetBytes || run.raw >= L1MaxRawBytes || run.closed
		old := now.Sub(oldestCreated(run.records)) >= L0PromoteAge
		switch {
		case len(run.records) >= 2 && (full || old || len(run.records) >= L0MaxFiles):
			plan.Merges = append(plan.Merges, CompactionGroup{Records: run.records, Level: LevelCompacted})
		case len(run.records) == 1 && (old || run.bytes >= L1TargetBytes):
			plan.Promote = append(plan.Promote, run.records[0])
		}
	}

	for _, run := range runs(l1, L2MaxBytes, 0, true) {
		dayOver := !now.Before(utcDay(run.records[0].MinTime).Add(24*time.Hour + dayCloseGrace))
		// A lone L1 of an ended day is rewritten too, so every day ends cold:
		// rollups (metrics) and trace sampling apply only there.
		if dayOver || (len(run.records) >= 2 && run.bytes >= L2MinBytes) {
			plan.Merges = append(plan.Merges, CompactionGroup{Records: run.records, Level: LevelCold})
		}
	}
	plan.Merges = withinBudget(plan.Merges)
	return plan
}

// runs splits records into consecutive runs of at most capBytes (and
// capRawBytes of raw size, 0 for none), legacy or not and, with
// splitByDay, one UTC day of earliest rows.
func runs(records []*SegmentRecord, capBytes, capRawBytes int64, splitByDay bool) []run {
	var result []run
	var current run
	for _, record := range records {
		overCap := current.bytes+record.SizeBytes > capBytes ||
			(capRawBytes > 0 && current.raw+record.RawSizeBytes > capRawBytes)
		newDay := splitByDay && len(current.records) > 0 &&
			!utcDay(record.MinTime).Equal(utcDay(current.records[0].MinTime))
		if len(current.records) > 0 && (overCap || newDay || record.IsLegacy() != current.records[0].IsLegacy()) {
			current.closed = overCap
			result = append(result, current)
			current = run{}
		}
		current.records = append(current.records, record)
		current.bytes += record.SizeBytes
		current.raw += record.RawSizeBytes
	}
	if len(current.records) > 0 {
		result = append(result, current)
	}
	return result
}

// withinBudget keeps merges, oldest first, until PassBudgetBytes of input;
// at least one, however large.
func withinBudget(merges []CompactionGroup) []CompactionGroup {
	var total int64
	for i, group := range merges {
		for _, record := range group.Records {
			total += record.SizeBytes
		}
		if total > PassBudgetBytes && i > 0 {
			return merges[:i]
		}
	}
	return merges
}

func oldestCreated(records []*SegmentRecord) time.Time {
	oldest := records[0].CreatedAt
	for _, record := range records[1:] {
		if record.CreatedAt.Before(oldest) {
			oldest = record.CreatedAt
		}
	}
	return oldest
}

func utcDay(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}
