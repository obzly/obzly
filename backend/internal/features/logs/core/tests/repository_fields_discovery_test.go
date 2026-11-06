package logs_core_tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	logs_core "obzly/internal/features/logs/core"
)

func Test_DiscoverFields_WithStoredLogsContainingCustomFields_ReturnsDiscoveredFields(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	customTestFields := map[string]any{
		"test_session": uniqueTestSession,
		"custom_field": "value_" + uniqueTestSession,
		"status_code":  200,
	}

	testLogEntries := CreateTestLogEntriesWithMessageAndFields(
		projectID,
		currentTime,
		fmt.Sprintf("Test log for field discovery - %s", uniqueTestSession),
		customTestFields,
	)

	storeErr := repository.StoreLogsBatch(testLogEntries)
	assert.NoError(t, storeErr)

	flushErr := repository.ForceFlush()
	assert.NoError(t, flushErr)

	discoveredFields, discoveryErr := repository.DiscoverFields(projectID)

	assert.NoError(t, discoveryErr)
	assert.NotNil(t, discoveredFields)
	assert.IsType(t, []string{}, discoveredFields)

	if len(discoveredFields) > 0 {
		discoveredFieldsMap := make(map[string]bool)
		for _, fieldName := range discoveredFields {
			discoveredFieldsMap[fieldName] = true
		}

		hasCustomTestFields := discoveredFieldsMap["test_session"] || discoveredFieldsMap["custom_field"] ||
			discoveredFieldsMap["status_code"]
		if hasCustomTestFields {
			t.Logf("Successfully discovered custom fields")
		}
	}
}

func Test_DiscoverFields_WithUnavailableLogsStorage_PropagatesError(t *testing.T) {
	unavailableRepository := logs_core.GetUnavailableLogCoreRepository()
	projectID := uuid.New()

	discoveredFields, discoveryErr := unavailableRepository.DiscoverFields(projectID)

	assert.Error(t, discoveryErr)
	assert.Nil(t, discoveredFields)
	assert.Contains(t, discoveryErr.Error(), "logs storage is unavailable")
}

// An attribute sent just before the newest 2,000 rows must still show in the
// sidebar.
func Test_DiscoverFields_AttributeOnlyBeforeTheNewest2000Rows_IsStillFound(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	now := time.Now().UTC()
	entries := map[uuid.UUID][]*logs_core.LogItem{projectID: {{
		ID: uuid.New(), ProjectID: projectID, Timestamp: now.Add(-time.Minute),
		Level: logs_core.LogLevelInfo, Message: "older", Fields: map[string]any{"rare_attribute": "x"},
	}}}
	for i := range 3000 {
		entries[projectID] = append(entries[projectID], &logs_core.LogItem{
			ID: uuid.New(), ProjectID: projectID, Timestamp: now.Add(time.Duration(i) * time.Millisecond),
			Level: logs_core.LogLevelInfo, Message: "newer", Fields: map[string]any{"common": "y"},
		})
	}
	require.NoError(t, repository.StoreLogsBatch(entries))
	require.NoError(t, repository.ForceFlush())

	discovered, err := repository.DiscoverFields(projectID)

	require.NoError(t, err)
	require.Contains(t, discovered, "rare_attribute")
	require.Contains(t, discovered, "common")
}

// Discovery reads only the newest segments, not the project's history.
func Test_DiscoverFields_ReadsOnlyTheNewestSegments(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	start := time.Now().UTC().Add(-48 * time.Hour)
	for i := range 25 {
		field := "recent"
		if i == 0 {
			field = "ancient"
		}
		require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {{
			ID: uuid.New(), ProjectID: projectID, Timestamp: start.Add(time.Duration(i) * time.Hour),
			Level: logs_core.LogLevelInfo, Message: "m", Fields: map[string]any{field: "v"},
		}}}))
		require.NoError(t, repository.ForceFlush())
	}

	discovered, err := repository.DiscoverFields(projectID)

	require.NoError(t, err)
	require.Contains(t, discovered, "recent")
	require.NotContains(t, discovered, "ancient", "the oldest of 25 segments is past the newest 20")
}
