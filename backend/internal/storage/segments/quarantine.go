package segments

import (
	"obzly/internal/util/logger"
)

// MaxCompactionAttempts: a segment whose merge failed, or crashed the process,
// this many times is left out of compaction, so one bad file can't stall a
// project's compaction every pass.
const MaxCompactionAttempts = 3

// Compactable drops the segments that reached MaxCompactionAttempts. They
// stay queryable, just never merged.
func Compactable(records []*SegmentRecord) []*SegmentRecord {
	kept := make([]*SegmentRecord, 0, len(records))
	var skipped []string
	for _, record := range records {
		if record.CompactionAttempts >= MaxCompactionAttempts {
			skipped = append(skipped, record.ID.String())
			continue
		}
		kept = append(kept, record)
	}
	if len(skipped) > 0 {
		logger.GetLogger().Warn("segments left out of compaction after repeated failed merges",
			"stream", records[0].Stream, "projectId", records[0].ProjectID,
			"count", len(skipped), "segmentIds", skipped[:min(len(skipped), 10)])
	}
	return kept
}
