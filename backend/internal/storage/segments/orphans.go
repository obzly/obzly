package segments

import (
	"time"

	"obzly/internal/storage"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// A segment's file lives on its writer's disk until uploaded, so when the
// writer's disk is gone (an evicted or replaced pod's emptyDir) a pending
// row can never be uploaded: every pass retries it, queries skip it, the
// upgrade check counts it as a blocker, and compaction skips any group that
// holds it - for spans with sampling, its whole day.
const (
	// WriterHeartbeatInterval: how often a process with a writer id marks
	// itself alive in segment_writers.
	WriterHeartbeatInterval = time.Minute
	// writerGoneAfter: a writer not seen this long is gone.
	writerGoneAfter = 10 * time.Minute
	// orphanAge: uploads run within seconds of a flush and are retried every
	// pass, so a pending row this old from a gone writer won't upload.
	orphanAge = time.Hour
	// unownedOrphanAge: rows from before writer_id existed; any replica may
	// hold them, so they get much longer.
	unownedOrphanAge = 24 * time.Hour

	orphanReason = "never uploaded; its writer's disk is gone"
)

// HeartbeatWriter marks writerID alive.
func HeartbeatWriter(writerID string, now time.Time) error {
	return storage.GetDb().Exec(`INSERT INTO segment_writers (id, last_seen_at) VALUES (?, ?)
		ON CONFLICT (id) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at`, writerID, now).Error
}

// MarkOrphansLost moves stream's pending segments whose writer is gone to
// lost_segments, returning how many. Only for installs with object storage:
// without it nothing is ever uploaded.
func (r *SegmentRepository) MarkOrphansLost(stream string, now time.Time) (int64, error) {
	return markOrphansLost(stream, uuid.Nil, now)
}

// markOrphansLost is MarkOrphansLost limited to projectID unless it is
// uuid.Nil (tests share one catalog).
func markOrphansLost(stream string, projectID uuid.UUID, now time.Time) (int64, error) {
	result := storage.GetDb().Exec(`
		WITH orphans AS (
			DELETE FROM stream_segments s
			WHERE s.stream = ? AND (?::uuid IS NULL OR s.project_id = ?) AND s.uploaded_at IS NULL AND (
				(s.writer_id IS NULL AND s.created_at < ?) OR
				(s.writer_id IS NOT NULL AND s.created_at < ? AND NOT EXISTS (
					SELECT 1 FROM segment_writers w WHERE w.id = s.writer_id AND w.last_seen_at > ?)))
			RETURNING s.id, s.project_id, s.stream, s.object_key, s.row_count, s.size_bytes, s.min_time, s.max_time
		)
		INSERT INTO lost_segments (id, project_id, stream, object_key, row_count, size_bytes, min_time, max_time, reason)
		SELECT id, project_id, stream, object_key, row_count, size_bytes, min_time, max_time, ? FROM orphans
		ON CONFLICT (id) DO NOTHING`,
		stream, projectScope(projectID), projectScope(projectID),
		now.Add(-unownedOrphanAge), now.Add(-orphanAge), now.Add(-writerGoneAfter), orphanReason)
	if result.Error == nil && result.RowsAffected > 0 {
		logger.GetLogger().Warn("moved never-uploaded segments of a gone writer to lost_segments",
			"stream", stream, "count", result.RowsAffected)
	}
	return result.RowsAffected, result.Error
}

func projectScope(projectID uuid.UUID) any {
	if projectID == uuid.Nil {
		return nil
	}
	return projectID
}
