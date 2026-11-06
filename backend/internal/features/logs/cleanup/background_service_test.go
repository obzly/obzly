package logs_cleanup

import (
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// Test_CalculateCutoffTimeForLogCount_OffsetQueryFindsNothing_ReturnsErrorNotNow:
// a cutoff of "now" would make DeleteOldLogs wipe the whole project, so when
// stats claim more logs than the offset query finds, it must error instead.
func Test_CalculateCutoffTimeForLogCount_OffsetQueryFindsNothing_ReturnsErrorNotNow(t *testing.T) {
	service := GetLogCleanupBackgroundService()
	projectID := uuid.New() // no logs ever stored for this project

	stats := &logs_core.LogsStatsDTO{
		TotalLogs:     5,
		OldestLogTime: time.Now().UTC().Add(-time.Hour),
		NewestLogTime: time.Now().UTC(),
	}

	before := time.Now().UTC()
	cutoff, err := service.calculateCutoffTimeForLogCount(projectID, 3, stats)
	after := time.Now().UTC()

	assert.Error(t, err, "an offset query that can't find the claimed logs must be a hard error, not a guess")
	assert.True(
		t,
		cutoff.IsZero(),
		"no cutoff should be returned alongside an error - a caller that ignored err must not accidentally get a usable-looking time.Time",
	)
	// Never "now" (which deletes everything), even if a future change stops
	// zeroing the value on error.
	assert.False(
		t,
		!cutoff.Before(before) && !cutoff.After(after),
		"cutoff must never silently be approximately time.Now()",
	)
}

// Test_CalculateCutoffTimeForLogCount_NormalCase_ReturnsMidpointBetweenLogs is
// the happy path.
func Test_CalculateCutoffTimeForLogCount_NormalCase_ReturnsMidpointBetweenLogs(t *testing.T) {
	service := GetLogCleanupBackgroundService()
	repo := logs_core.GetLogCoreRepository()
	projectID := uuid.New()

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	var entries []*logs_core.LogItem
	for i := range 5 {
		entries = append(entries, &logs_core.LogItem{
			ID:        uuid.New(),
			ProjectID: projectID,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Level:     logs_core.LogLevelInfo,
			Message:   "cutoff calc test",
		})
	}
	for _, e := range entries {
		assert.NoError(t, repo.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {e}}))
	}
	assert.NoError(t, repo.ForceFlush())

	stats, err := repo.GetProjectLogStats(projectID)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), stats.TotalLogs)

	// Delete the oldest 2 of 5 - cutoff should land strictly between
	// entries[1] (last to delete) and entries[2] (first to keep).
	cutoff, err := service.calculateCutoffTimeForLogCount(projectID, 2, stats)
	assert.NoError(t, err)
	assert.True(t, cutoff.After(entries[1].Timestamp))
	assert.True(t, cutoff.Before(entries[2].Timestamp))
}
