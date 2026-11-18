package correlation_worker_test

import (
	"encoding/json"
	"testing"
	"time"

	"obzly/internal/features/alerting"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The scanner queues one "incident opened" notification for a session past
// the project's thresholds, and none again when it rescans the window.
func Test_CorrelateProject_WithIncidentNotifications_QueuesTheOpeningOnce(t *testing.T) {
	projectID := createTestProject(t)
	channelID := uuid.New()
	require.NoError(t, storage.GetDb().Exec(`INSERT INTO notification_channels
		(id, name, kind, config_encrypted, source, all_projects, project_ids)
		VALUES (?, ?, 'webhook', 'x', 'local', FALSE, ?::jsonb)`,
		channelID, "incident-hook "+channelID.String()[:8], `["`+projectID.String()+`"]`).Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channelID) })
	now := time.Now().UTC()
	require.NoError(t, storage.GetDb().Save(&alerting.IncidentNotificationSettings{
		ProjectID: projectID, NotifyOpen: true, MinSignals: 2, MinServices: 5, Severity: alerting.SeverityHigh,
		ChannelIDs: alerting.UUIDList{channelID}, UpdatedAt: &now,
	}).Error)

	storeErrorLog(t, projectID, "checkout", now.Add(-time.Minute), "")
	storeErrorLog(t, projectID, "checkout", now.Add(-50*time.Second), "")
	runWorker(t, projectID)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM correlation_watermarks WHERE project_id = ?", projectID).Error)
	runWorker(t, projectID) // rescans the same window: duplicate sessions join the incident

	var payloads []string
	require.NoError(t, storage.GetDb().Raw(`SELECT payload::text FROM notification_outbox WHERE project_id = ?`,
		projectID).Scan(&payloads).Error)
	require.Len(t, payloads, 1)
	var payload alerting.Notification
	require.NoError(t, json.Unmarshal([]byte(payloads[0]), &payload))
	require.Equal(t, alerting.KindIncident, payload.Kind)
	require.Equal(t, alerting.StatusFiring, payload.Status)
	require.Equal(t, []string{"checkout"}, payload.Incident.Services)
	require.GreaterOrEqual(t, payload.Incident.SignalCount, 2)
}
