package alerting

import (
	"context"
	"testing"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// An incident notifies once when it crosses its thresholds, however many
// sessions the scanner folds into it, and once when it ends.
func Test_IncidentNotifications_CrossingThresholds_NotifiesOpenAndEndOnce(t *testing.T) {
	rec := newReceiver(t)
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	saveIncidentSettings(t, f, true, true, 5, 3)
	start := time.Now().UTC().Add(-10 * time.Minute)

	// Below both thresholds: tracked, not notified.
	first := observed([]string{"checkout"}, 2, 1, start)
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{first}, start))
	require.Empty(t, f.outbox(t))

	// A session sharing checkout within the quiet period joins it and
	// crosses minSignals (3 + 3 >= 5).
	second := observed([]string{"checkout", "payments"}, 2, 1, start.Add(time.Minute))
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{second}, start.Add(time.Minute)))
	items := f.outbox(t)
	require.Len(t, items, 1)
	require.Equal(t, StatusFiring, items[0].Status)
	require.Nil(t, items[0].RuleID)
	payload := items[0].Payload
	require.Equal(t, KindIncident, payload.Kind)
	require.Equal(t, SeverityCritical, payload.Severity)
	require.Equal(t, first.ID, payload.Incident.ID, "the link opens the incident's first session")
	require.Equal(t, []string{"checkout", "payments"}, payload.Incident.Services)
	require.Equal(t, 6, payload.Incident.SignalCount)

	// More sessions of the same incident: no new notification.
	third := observed([]string{"payments"}, 10, 0, start.Add(2*time.Minute))
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{third}, start.Add(2*time.Minute)))
	require.Len(t, f.outbox(t), 1)

	// Still within the quiet period: not ended.
	require.NoError(t, EndQuietIncidents(third.WindowEnd.Add(incidentQuietPeriod-time.Minute)))
	require.Len(t, f.outbox(t), 1)
	ended := third.WindowEnd.Add(incidentQuietPeriod + time.Minute)
	require.NoError(t, EndQuietIncidents(ended))
	require.NoError(t, EndQuietIncidents(ended.Add(time.Minute)))
	items = f.outbox(t)
	require.Len(t, items, 2)
	require.Equal(t, StatusResolved, items[1].Status)
	require.Equal(t, items[0].Fingerprint, items[1].Fingerprint, "PagerDuty resolves the same incident")
	require.Equal(t, 16, items[1].Payload.Incident.SignalCount)

	// Delivered through the outbox like any alert.
	f.clock = ended.Add(time.Minute)
	_, err := f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	payloads := rec.payloads()
	require.Len(t, payloads, 2)
	require.Equal(t, "incident", payloads[0]["kind"])
	require.Equal(t, "critical", payloads[0]["severity"])
}

// Enough services alone open an incident; a session sharing none of its
// services, or starting after the quiet period, is another incident.
func Test_IncidentNotifications_ServicesThresholdAndSeparateIncidents(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	saveIncidentSettings(t, f, true, false, 1000, 2)
	start := time.Now().UTC().Add(-time.Hour)

	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{
		observed([]string{"a", "b"}, 1, 0, start),
		observed([]string{"c", "d"}, 1, 0, start),
		observed([]string{"a", "b"}, 1, 0, start.Add(incidentQuietPeriod+10*time.Minute)),
	}, start))
	require.Len(t, f.outbox(t), 3)

	// notifyEnd off: ending queues nothing.
	require.NoError(t, EndQuietIncidents(time.Now().UTC()))
	require.Len(t, f.outbox(t), 3)
}

// Off (or never set): sessions aren't even tracked.
func Test_IncidentNotifications_WhenOff_QueuesNothing(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	start := time.Now().UTC()
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{
		observed([]string{"a", "b", "c"}, 50, 50, start),
	}, start))
	saveIncidentSettings(t, f, false, false, 1, 1)
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{
		observed([]string{"a", "b", "c"}, 50, 50, start),
	}, start))
	require.Empty(t, f.outbox(t))
	var tracked int64
	require.NoError(t, storage.GetDb().Model(&incidentRecord{}).Where("project_id = ?", f.projectID).
		Count(&tracked).Error)
	require.Zero(t, tracked)
}

// A silence matching an incident notification's labels mutes it.
func Test_IncidentNotifications_MatchingSilence_MutesIt(t *testing.T) {
	rec := newReceiver(t)
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	saveIncidentSettings(t, f, true, false, 1, 1)
	require.NoError(t, f.repo.saveSilence(&Silence{
		ID: uuid.New(), ProjectID: f.projectID, Matchers: Matchers{{Name: "alertname", Value: incidentAlertName}},
		StartsAt: f.clock.Add(-time.Minute), EndsAt: f.clock.Add(time.Hour), CreatedAt: f.clock,
	}))
	require.NoError(t, ObserveIncidentSessions(f.projectID, []ObservedSession{
		observed([]string{"a"}, 3, 0, f.clock),
	}, f.clock))
	_, err := f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Empty(t, rec.payloads())
	require.Equal(t, "silenced", f.outbox(t)[0].LastError)
}

func saveIncidentSettings(t *testing.T, f *fixture, open, end bool, minSignals, minServices int) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, storage.GetDb().Save(&IncidentNotificationSettings{
		ProjectID: f.projectID, NotifyOpen: open, NotifyEnd: end, MinSignals: minSignals, MinServices: minServices,
		Severity: SeverityCritical, ChannelIDs: UUIDList{f.channel.ID}, UpdatedAt: &now,
	}).Error)
}

func observed(services []string, errorLogs, errorSpans int, start time.Time) ObservedSession {
	return ObservedSession{
		ID: uuid.New(), Services: services, ErrorLogs: errorLogs, ErrorSpans: errorSpans,
		WindowStart: start, WindowEnd: start.Add(30 * time.Second),
	}
}
