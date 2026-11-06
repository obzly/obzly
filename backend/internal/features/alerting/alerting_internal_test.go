package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"obzly/internal/features/sharedconfig"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/storage"
	"obzly/internal/util/egress"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// receiver is a webhook endpoint recording what it gets.
type receiver struct {
	mu       sync.Mutex
	received []map[string]any
	status   int
	server   *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{status: http.StatusOK}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		r.mu.Lock()
		r.received = append(r.received, payload)
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) payloads() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.received...)
}

// loopbackSender may reach the test receiver (loopback is otherwise
// refused).
func loopbackSender(t *testing.T) *sender {
	allow, err := egress.ParseCIDRs("127.0.0.0/8")
	require.NoError(t, err)
	return &sender{client: egress.NewClient(egress.PublicOnly(allow, nil), sendTimeout), email: sharedconfig.SendMail}
}

type fixture struct {
	projectID  uuid.UUID
	channel    *NotificationChannel
	repo       *repository
	evaluator  *Evaluator
	dispatcher *Dispatcher
	clock      time.Time
}

func newFixture(t *testing.T, receiverURL string, s *sender) *fixture {
	t.Helper()
	projectID := uuid.New()
	require.NoError(t, storage.GetDb().Exec("INSERT INTO projects (id, name) VALUES (?, ?)",
		projectID, "alerting "+projectID.String()[:8]).Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM projects WHERE id = ?", projectID) })
	f := &fixture{projectID: projectID, repo: &repository{}, clock: time.Now().UTC()}
	now := func() time.Time { return f.clock }
	f.evaluator = &Evaluator{repo: f.repo, log: logger.GetLogger(), now: now}
	secrets := &users_repositories.SecretKeyRepository{}
	f.dispatcher = &Dispatcher{repo: f.repo, secrets: secrets, sender: s, log: logger.GetLogger(), now: now}
	encoded, _ := json.Marshal(ChannelConfig{URL: receiverURL})
	encrypted, err := secrets.Encrypt(string(encoded))
	require.NoError(t, err)
	f.channel = &NotificationChannel{
		ID:              uuid.New(),
		Name:            "hook",
		Kind:            ChannelWebhook,
		ConfigEncrypted: encrypted,
		Source:          sharedconfig.SourceLocal,
		ProjectIDs:      ProjectIDs{projectID.String()},
		CreatedAt:       f.clock,
		UpdatedAt:       f.clock,
	}
	require.NoError(t, f.repo.saveChannel(f.channel))
	return f
}

func (f *fixture) rule(t *testing.T, promql string, forSeconds, repeatSeconds int) *AlertRule {
	t.Helper()
	rule := &AlertRule{
		ID: uuid.New(), ProjectID: f.projectID, Name: "rule " + uuid.NewString()[:6], Enabled: true,
		Source: SourceMetrics, Query: RuleQuery{PromQL: promql}, Comparison: ">", Threshold: 0,
		WindowSeconds: 300, IntervalSeconds: 15, ForSeconds: forSeconds, RepeatSeconds: repeatSeconds,
		Labels: Labels{"team": "core"}, ChannelIDs: []uuid.UUID{f.channel.ID}, CreatedAt: f.clock, UpdatedAt: f.clock,
	}
	require.NoError(t, f.repo.saveRule(rule))
	return rule
}

// advanceWith runs one evaluation of samples at the fixture's clock.
func (f *fixture) advanceWith(t *testing.T, rule *AlertRule, samples ...sample) {
	t.Helper()
	require.NoError(t, storage.GetDb().Transaction(func(tx *gorm.DB) error {
		return f.evaluator.advance(tx, rule, samples, f.clock)
	}))
}

func (f *fixture) outbox(t *testing.T) []*OutboxItem {
	t.Helper()
	var items []*OutboxItem
	require.NoError(t, storage.GetDb().Where("project_id = ?", f.projectID).Order("created_at").Find(&items).Error)
	return items
}

// The Prometheus state machine: pending until ForSeconds, then firing
// (one notification, repeated after RepeatSeconds), resolved when the
// condition stops holding (another); a pending series that stops is
// dropped without a word.
func Test_Advance_PendingFiringRepeatResolved(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "vector(1)", 60, 3600)
	web := sample{labels: Labels{"service": "web"}, value: 5}
	api := sample{labels: Labels{"service": "api"}, value: 7}
	below := sample{labels: Labels{"service": "web"}, value: -1}

	f.advanceWith(t, rule, web, api)
	states, err := f.repo.states(rule.ID)
	require.NoError(t, err)
	require.Len(t, states, 2)
	for _, state := range states {
		require.Equal(t, StatePending, state.State)
		require.Equal(t, "core", state.Labels["team"])
		require.Equal(t, rule.Name, state.Labels["alertname"])
	}
	require.Empty(t, f.outbox(t))

	f.clock = f.clock.Add(30 * time.Second)
	f.advanceWith(t, rule, web) // api stops while pending: dropped
	states, _ = f.repo.states(rule.ID)
	require.Len(t, states, 1)

	f.clock = f.clock.Add(31 * time.Second)
	f.advanceWith(t, rule, web)
	states, _ = f.repo.states(rule.ID)
	require.Equal(t, StateFiring, states[0].State)
	require.Len(t, f.outbox(t), 1)

	f.clock = f.clock.Add(10 * time.Minute)
	f.advanceWith(t, rule, web)
	require.Len(t, f.outbox(t), 1, "no repeat within RepeatSeconds")

	f.clock = f.clock.Add(time.Hour)
	f.advanceWith(t, rule, web)
	require.Len(t, f.outbox(t), 2, "repeated after RepeatSeconds")

	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, rule, below)
	items := f.outbox(t)
	require.Len(t, items, 3)
	require.Equal(t, StatusResolved, items[2].Status)
	require.NotNil(t, items[2].Payload.EndsAt)
	states, _ = f.repo.states(rule.ID)
	require.Equal(t, StateResolved, states[0].State)
}

// Delivery: sent, silenced by a matching silence, retried with backoff on
// a receiver error and given up after maxAttempts; and the default egress
// policy refuses a receiver on a private address.
func Test_Dispatch_SendsSilencesRetriesAndRefusesPrivateReceivers(t *testing.T) {
	rec := newReceiver(t)
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	rule := f.rule(t, "vector(1)", 0, 0)
	f.advanceWith(t, rule, sample{labels: Labels{"service": "web"}, value: 1})
	_, err := f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	payloads := rec.payloads()
	require.Len(t, payloads, 1)
	require.Equal(t, "firing", payloads[0]["status"])
	require.Equal(t, rule.Name, payloads[0]["ruleName"])

	// Silenced: marked handled, nothing sent.
	require.NoError(t, f.repo.saveSilence(&Silence{
		ID: uuid.New(), ProjectID: f.projectID, Matchers: Matchers{{Name: "service", Value: "w.*", Regex: true}},
		StartsAt: f.clock.Add(-time.Minute), EndsAt: f.clock.Add(time.Hour), CreatedAt: f.clock,
	}))
	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, rule, sample{labels: Labels{"service": "web"}, value: -1}) // resolves
	_, err = f.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, rec.payloads(), 1)
	items := f.outbox(t)
	require.Equal(t, "silenced", items[1].LastError)
	require.NotNil(t, items[1].SentAt)

	// A failing receiver: backoff, then given up.
	rec.mu.Lock()
	rec.status = http.StatusInternalServerError
	rec.mu.Unlock()
	f.advanceWith(t, rule, sample{labels: Labels{"service": "db"}, value: 1})
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, err = f.dispatcher.dispatchOnce(context.Background())
		require.NoError(t, err)
		items = f.outbox(t)
		last := items[len(items)-1]
		require.Equal(t, attempt, last.Attempts)
		require.Contains(t, last.LastError, "HTTP 500")
		if attempt < maxAttempts {
			require.Nil(t, last.FailedAt)
			require.True(t, last.NextAttemptAt.After(f.clock))
			f.clock = last.NextAttemptAt
		} else {
			require.NotNil(t, last.FailedAt)
		}
	}

	// The default policy: a loopback receiver is refused before any request.
	refused := newFixture(t, rec.server.URL, newSender())
	before := len(rec.payloads())
	refusedRule := refused.rule(t, "vector(1)", 0, 0)
	refused.advanceWith(t, refusedRule, sample{labels: Labels{}, value: 1})
	_, err = refused.dispatcher.dispatchOnce(context.Background())
	require.NoError(t, err)
	require.Contains(t, refused.outbox(t)[0].LastError, "not public")
	require.Len(t, rec.payloads(), before)
}

// Rule to notification under 60 s, with the real evaluation and dispatch loops
// (leader-locked, ticking).
func Test_RuleToNotification_UnderAMinute(t *testing.T) {
	rec := newReceiver(t)
	f := newFixture(t, rec.server.URL, loopbackSender(t))
	f.evaluator.now = func() time.Time { return time.Now().UTC() }
	f.dispatcher.now = f.evaluator.now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	created := map[string]time.Time{}
	for range 5 {
		rule := f.rule(t, "vector(1)", 0, 0)
		created[rule.Name] = time.Now()
	}
	go f.evaluator.loop(ctx, 0, 1)
	f.dispatcher.Start(ctx)

	deadline := time.Now().Add(60 * time.Second)
	for len(rec.payloads()) < len(created) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	payloads := rec.payloads()
	require.Len(t, payloads, len(created), "every rule notified within a minute")
	for _, payload := range payloads {
		name, _ := payload["ruleName"].(string)
		latency := time.Since(created[name])
		require.Less(t, latency, 60*time.Second)
		t.Logf("%s notified after %s", name, latency.Round(100*time.Millisecond))
	}
}

func (r *repository) states(ruleID uuid.UUID) ([]*AlertState, error) {
	var states []*AlertState
	err := storage.GetDb().Where("rule_id = ?", ruleID).Find(&states).Error
	return states, err
}
