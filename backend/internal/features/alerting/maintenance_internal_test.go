package alerting

import (
	"testing"
	"time"

	"obzly/internal/features/sharedconfig"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A maintenance window's managed silence: a firing synthetics rule doesn't
// notify while it lasts, and still firing after it, notifies at the next
// evaluation (even with no repeat interval).
func Test_Advance_ManagedRuleInMaintenance_HeldDuringTheWindow_NotifiesAfterIt(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM silences WHERE managed_by <> ''") })
	checkID := uuid.NewString()
	rule := f.rule(t, "vector(1)", 0, 0)
	rule.ManagedBy = "synthetics:" + checkID + ":down"
	rule.Labels = Labels{"severity": "critical", "source": "synthetics", "check_id": checkID}
	require.NoError(t, f.repo.saveRule(rule))
	other := f.rule(t, "vector(1)", 0, 0)
	other.Labels = Labels{"check_id": uuid.NewString()}
	require.NoError(t, f.repo.saveRule(other))
	down := sample{labels: Labels{}, value: 1}
	windowEnd := f.clock.Add(time.Hour)
	require.NoError(t, ApplyManagedSilences([]sharedconfig.MaintenanceSilence{{
		Key: "synthetics-maintenance:w:" + f.projectID.String() + ":1", ProjectID: f.projectID.String(),
		Comment: "Synthetics maintenance: Weekly DB patching", CheckIDs: []string{checkID, uuid.NewString()},
		StartsAt: f.clock.Add(-time.Minute), EndsAt: windowEnd,
	}}))

	f.advanceWith(t, rule, down)
	f.advanceWith(t, other, down)
	items := f.outbox(t)
	require.Len(t, items, 1, "only the rule outside the window notifies")
	require.Equal(t, other.ID, *items[0].RuleID)
	states, err := f.repo.states(rule.ID)
	require.NoError(t, err)
	require.Equal(t, StateFiring, states[0].State, "it still fires: the status stays true")
	require.Nil(t, states[0].LastNotifiedAt)

	f.clock = f.clock.Add(30 * time.Minute)
	f.advanceWith(t, rule, down)
	require.Len(t, f.outbox(t), 1, "no notification later in the window either")

	f.clock = windowEnd.Add(time.Second)
	f.advanceWith(t, rule, down)
	items = f.outbox(t)
	require.Len(t, items, 2, "still down after the window: notifies at the first evaluation")
	require.Equal(t, rule.ID, *items[1].RuleID)
	require.Equal(t, StatusFiring, items[1].Status)

	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, rule, down)
	require.Len(t, f.outbox(t), 2, "once, as before the window")
}
