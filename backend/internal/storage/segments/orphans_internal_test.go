package segments

import (
	"testing"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_MarkOrphansLost_MovesOldPendingSegmentsOfGoneWritersOnly(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()
	live, gone := "live-"+uuid.NewString(), "gone-"+uuid.NewString()
	require.NoError(t, HeartbeatWriter(live, now))
	require.NoError(t, HeartbeatWriter(gone, now.Add(-time.Hour)))
	orphan := pendingSegment(t, projectID, &gone, 2*time.Hour, false)
	unownedOrphan := pendingSegment(t, projectID, nil, 25*time.Hour, false)
	keep := []uuid.UUID{
		pendingSegment(t, projectID, &live, 2*time.Hour, false),    // its writer may still upload it
		pendingSegment(t, projectID, &gone, 10*time.Minute, false), // too young to tell
		pendingSegment(t, projectID, nil, 2*time.Hour, false),      // any replica may hold it
		pendingSegment(t, projectID, &gone, 2*time.Hour, true),     // already uploaded
	}

	lost, err := markOrphansLost("logs", projectID, now)

	require.NoError(t, err)
	require.EqualValues(t, 2, lost)
	remaining, err := (&SegmentRepository{}).ListByProject(projectID, "logs")
	require.NoError(t, err)
	var remainingIDs []uuid.UUID
	for _, record := range remaining {
		remainingIDs = append(remainingIDs, record.ID)
	}
	require.ElementsMatch(t, keep, remainingIDs)
	var reasons []string
	require.NoError(t, storage.GetDb().Raw("SELECT reason FROM lost_segments WHERE id IN ?",
		[]uuid.UUID{orphan, unownedOrphan}).Scan(&reasons).Error)
	require.Equal(t, []string{orphanReason, orphanReason}, reasons)
}

func Test_HeartbeatWriter_AgainUpdatesLastSeen(t *testing.T) {
	writer := "w-" + uuid.NewString()
	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	second := first.Add(30 * time.Minute)
	require.NoError(t, HeartbeatWriter(writer, first))

	require.NoError(t, HeartbeatWriter(writer, second))

	var lastSeen time.Time
	require.NoError(t, storage.GetDb().Raw("SELECT last_seen_at FROM segment_writers WHERE id = ?", writer).
		Scan(&lastSeen).Error)
	require.True(t, second.Equal(lastSeen), "got %s", lastSeen)
}

func pendingSegment(t *testing.T, projectID uuid.UUID, writerID *string, age time.Duration, uploaded bool) uuid.UUID {
	t.Helper()
	now := time.Now().UTC()
	record := &SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs",
		Path: "/data/" + uuid.NewString() + ".parquet", RowCount: 1, SizeBytes: 1,
		MinTime: now.Add(-age), MaxTime: now.Add(-age), CreatedAt: now.Add(-age),
		SchemaVersion: CurrentSchemaVersion, WriterID: writerID}
	if uploaded {
		record.UploadedAt = &now
		record.ObjectKey = "logs/" + record.ID.String()
	}
	require.NoError(t, storage.GetDb().Create(record).Error)
	return record.ID
}
