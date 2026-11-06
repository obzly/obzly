package alerting

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_RunShard_TwoRunnersAtOnce_EvaluateEachRuleOnce(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	f.evaluator.now = func() time.Time { return time.Now().UTC() }
	var rules []*AlertRule
	for range 4 {
		rules = append(rules, f.rule(t, "vector(1)", 0, 0))
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { require.NoError(t, f.evaluator.runShard(context.Background(), 0, 1)) })
	}
	wg.Wait()

	for _, rule := range rules {
		var count int64
		require.NoError(t, storage.GetDb().Model(&OutboxItem{}).Where("rule_id = ?", rule.ID).Count(&count).Error)
		require.Equal(t, int64(1), count, "one evaluation, so one notification, per rule")
	}
}

func Test_ClaimRule_SecondClaimOfTheSameEvaluation_Fails(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "vector(1)", 0, 0)

	claimed, err := f.repo.claimRule(rule.ID, rule.LastEvalAt, f.clock)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = f.repo.claimRule(rule.ID, rule.LastEvalAt, f.clock.Add(time.Second))
	require.NoError(t, err)
	require.False(t, claimed, "a runner holding a stale last_eval_at must not evaluate the rule again")
}

func Test_SendSlack_LabelsFromTelemetry_CantMentionOrLink(t *testing.T) {
	rec := newReceiver(t)
	s := loopbackSender(t)
	err := s.send(context.Background(), ChannelSlack, ChannelConfig{URL: rec.server.URL}, Notification{
		Status: StatusFiring, RuleName: "Errors <!here>", Summary: "a & b",
		Labels: Labels{"pattern": "<!channel> <https://evil.example.com|reset your password>"},
	})
	require.NoError(t, err)
	payloads := rec.payloads()
	require.Len(t, payloads, 1)
	text, _ := payloads[0]["text"].(string)
	require.NotContains(t, text, "<")
	require.NotContains(t, text, ">")
	require.Contains(t, text, "&lt;!channel&gt;")
	require.Contains(t, text, "a &amp; b")
}

func Test_Truncate_MultiByteAndInvalidInput_StaysValidUTF8(t *testing.T) {
	value := strings.Repeat("é", 10) // 2 bytes each
	cut := truncate(value, 5)
	require.True(t, utf8.ValidString(cut))
	require.Equal(t, "éé", cut)
	require.True(t, utf8.ValidString(truncate("ok\xff\xfe"+value, 6)))
	require.Equal(t, "short", truncate("short", 100))
}

func Test_Dispatch_FailingChannel_IsPausedThenRetried(t *testing.T) {
	rec := newReceiver(t)
	rec.status = http.StatusInternalServerError
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	rule := f.rule(t, "vector(1)", 0, 0)
	var samples []sample
	for i := range breakerFailures + 2 {
		samples = append(samples, sample{labels: Labels{"n": string(rune('a' + i))}, value: 1})
	}
	f.advanceWith(t, rule, samples...)
	_, err := f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, rec.payloads(), breakerFailures+2)

	// Due again after the first backoff, but the channel is paused.
	f.clock = f.clock.Add(backoff(1) + time.Second)
	_, err = f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, rec.payloads(), breakerFailures+2, "no sends while the channel is paused")
	for _, item := range f.outbox(t) {
		require.Equal(t, 1, item.Attempts, "a paused delivery doesn't count as an attempt")
	}

	rec.mu.Lock()
	rec.status = http.StatusOK
	rec.mu.Unlock()
	f.clock = f.clock.Add(breakerPause)
	_, err = f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, rec.payloads(), 2*(breakerFailures+2))
	for _, item := range f.outbox(t) {
		require.NotNil(t, item.SentAt)
	}
}

func Test_ClaimOutbox_ClaimedRows_AreLeasedToOneDispatcher(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "vector(1)", 0, 0)
	f.advanceWith(t, rule, sample{labels: Labels{}, value: 1})

	first, err := f.repo.claimOutbox(1000, f.clock, f.clock.Add(claimLease))
	require.NoError(t, err)
	require.NotEmpty(t, first)
	second, err := f.repo.claimOutbox(1000, f.clock, f.clock.Add(claimLease))
	require.NoError(t, err)
	for _, item := range second {
		require.NotEqual(t, f.projectID, item.ProjectID, "a leased row isn't claimed again")
	}
	again, err := f.repo.claimOutbox(1000, f.clock.Add(claimLease), f.clock.Add(2*claimLease))
	require.NoError(t, err)
	var ours int
	for _, item := range again {
		if item.ProjectID == f.projectID {
			ours++
		}
	}
	require.Equal(t, len(first), ours, "an expired lease makes the row due again")
}

func Test_Prune_OldFinishedNotifications_AreDeletedRecentOnesKept(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	old := f.clock.Add(-keepOutbox - time.Hour)
	recent := f.clock.Add(-time.Hour)
	insert := func(sentAt, failedAt *time.Time) uuid.UUID {
		item := &OutboxItem{
			ID: uuid.New(), ProjectID: f.projectID, ChannelID: f.channel.ID, Status: StatusFiring,
			Payload: Notification{Status: StatusFiring}, NextAttemptAt: old, SentAt: sentAt, FailedAt: failedAt,
			CreatedAt: old,
		}
		require.NoError(t, storage.GetDb().Create(item).Error)
		return item.ID
	}
	oldSent, oldFailed := insert(&old, nil), insert(nil, &old)
	recentSent, pending := insert(&recent, nil), insert(nil, nil)

	require.NoError(t, f.dispatcher.prune(context.Background()))

	var left []uuid.UUID
	require.NoError(t, storage.GetDb().Model(&OutboxItem{}).Where("project_id = ?", f.projectID).
		Pluck("id", &left).Error)
	require.ElementsMatch(t, []uuid.UUID{recentSent, pending}, left)
	require.NotContains(t, left, oldSent)
	require.NotContains(t, left, oldFailed)
}
