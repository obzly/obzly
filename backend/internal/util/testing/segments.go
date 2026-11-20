package testing

import (
	"testing"
	"time"

	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// UseIsolatedSegmentWriter gives the test a fresh segment writer id: rows it writes
// carry it, and uploaders list only those. Without it, uploaders pick up other packages'
// pending rows (one shared Postgres) whose temp dirs are gone.
// It is marked alive, so another package's orphan sweep doesn't retire its pending
// rows as a gone writer's (AgeSegments makes them look old enough).
func UseIsolatedSegmentWriter(t *testing.T) {
	t.Helper()
	id := "test-" + uuid.NewString()
	segments.SetLocalWriter(id, false)
	t.Cleanup(segments.ResetLocalWriter)
	if err := segments.HeartbeatWriter(id, time.Now().UTC()); err != nil {
		t.Fatalf("failed to mark the test segment writer alive: %v", err)
	}
}

// AgeSegments makes a project's segments of stream look created age ago, so
// leveled compaction (segments.PlanCompaction) merges them now instead of
// waiting for more data.
func AgeSegments(t *testing.T, projectID uuid.UUID, stream string, age time.Duration) {
	t.Helper()
	err := storage.GetDb().Exec(
		"UPDATE stream_segments SET created_at = created_at - make_interval(secs => ?) WHERE project_id = ? AND stream = ?",
		age.Seconds(), projectID, stream,
	).
		Error
	if err != nil {
		t.Fatalf("failed to age segments: %v", err)
	}
}
