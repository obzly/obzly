package alerting

import (
	"context"
	"math/big"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SQLSamples_WithValueColumn_UsesItAndTheOtherColumnsAsLabels(t *testing.T) {
	samples, err := sqlSamples(
		[]string{"value", "service", "status", "p95"},
		[][]any{{int64(7), "checkout", int32(500), 1.5}, {nil, "search", int32(200), 2.5}},
	)

	require.NoError(t, err)
	require.Len(t, samples, 1, "a row without a value is no series")
	assert.Equal(t, 7.0, samples[0].value)
	assert.Equal(t, Labels{"service": "checkout", "status": "500", "p95": "1.5"}, samples[0].labels)
}

func Test_SQLSamples_WithoutValueColumn_UsesTheLastNumericColumn(t *testing.T) {
	samples, err := sqlSamples(
		[]string{"errors", "service", "count_star()"},
		[][]any{{int64(1), "checkout", big.NewInt(40)}, {int64(2), "search", big.NewInt(9)}},
	)

	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, 40.0, samples[0].value)
	assert.Equal(t, Labels{"errors": "1", "service": "checkout"}, samples[0].labels)
	assert.Equal(t, 9.0, samples[1].value)
}

func Test_SQLSamples_NumericTextOutsideTheValueColumn_IsALabel(t *testing.T) {
	samples, err := sqlSamples([]string{"status", "hits"}, [][]any{{"404", int64(3)}})

	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, 3.0, samples[0].value)
	assert.Equal(t, Labels{"status": "404"}, samples[0].labels)
}

func Test_SQLSamples_WithTimeColumn_KeepsEachLabelSetsLatestRow(t *testing.T) {
	samples, err := sqlSamples(
		[]string{"minute", "service", "n"},
		[][]any{
			{"2026-11-18T10:02:00Z", "checkout", int64(1)},
			{"2026-11-18T10:00:00Z", "checkout", int64(50)},
			{"2026-11-18T10:01:00Z", "search", int64(4)},
			{"2026-11-18T10:00:00Z", "search", int64(9)},
		},
	)

	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, Labels{"service": "checkout"}, samples[0].labels, "the time column is not a label")
	assert.Equal(t, 1.0, samples[0].value)
	assert.Equal(t, 4.0, samples[1].value)
}

func Test_SQLSamples_ColumnNames_BecomeLabelNames(t *testing.T) {
	samples, err := sqlSamples(
		[]string{"service name", "1st", "", "value"},
		[][]any{{"checkout", "a", nil, 1.0}},
	)

	require.NoError(t, err)
	assert.Equal(t, Labels{"service_name": "checkout", "_1st": "a", "column_3": ""}, samples[0].labels)
}

func Test_SQLSamples_WithoutANumber_ReturnsError(t *testing.T) {
	_, err := sqlSamples([]string{"service"}, [][]any{{"checkout"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must return a number")
}

func Test_SQLSamples_EmptyResult_HasNoSeries(t *testing.T) {
	samples, err := sqlSamples([]string{"service", "value"}, nil)

	require.NoError(t, err)
	assert.Empty(t, samples)
}

func Test_SQLSamples_MoreSeriesThanTheCap_ReturnsError(t *testing.T) {
	rows := make([][]any, maxSQLSeries+1)
	for i := range rows {
		rows[i] = []any{uuid.NewString(), int64(i)}
	}

	_, err := sqlSamples([]string{"id", "value"}, rows)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most 100")
}

// A logs_sql rule runs over the window and compares each service's newest
// minute: checkout had a burst three minutes ago but is quiet now.
func Test_Evaluate_LogsSQLRule_FiresOnEachLabelSetsLatestRow(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	minute := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	var logs []*logs_core.LogItem
	add := func(at time.Time, n int, service string) {
		for i := range n {
			logs = append(logs, &logs_core.LogItem{
				ID: uuid.New(), ProjectID: f.projectID, Timestamp: at.Add(time.Duration(i) * time.Second),
				Level: logs_core.LogLevelError, Message: "boom", Fields: map[string]any{"service_name": service},
			})
		}
	}
	add(minute.Add(-3*time.Minute), 6, "checkout")
	add(minute.Add(time.Second), 1, "checkout")
	add(minute.Add(time.Second), 4, "search")
	require.NoError(t,
		logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{f.projectID: logs}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
	f.clock = minute.Add(2 * time.Minute)

	rule := f.rule(t, "", 0, 0)
	rule.Source, rule.WindowSeconds = SourceLogsSQL, 300
	rule.Query = RuleQuery{SQL: "SELECT time_bucket(INTERVAL '1 minute', timestamp) AS minute, " +
		"json_extract_string(fields, '$.service_name') AS service, count(*) AS errors FROM logs GROUP BY ALL"}
	rule.Comparison, rule.Threshold = ">", 2
	require.NoError(t, f.repo.saveRule(rule))
	require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))

	stored, err := f.repo.rule(f.projectID, rule.ID)
	require.NoError(t, err)
	require.Empty(t, stored.LastError)
	states, err := f.repo.states(rule.ID)
	require.NoError(t, err)
	require.Len(t, states, 1, "%v", states)
	assert.Equal(t, "search", states[0].Labels["service"])
	assert.Equal(t, "core", states[0].Labels["team"], "the rule's labels are kept")
	assert.Equal(t, 4.0, states[0].Value)
	assert.Equal(t, StateFiring, states[0].State)
	require.Len(t, f.outbox(t), 1)
	assert.Equal(t, "search", f.outbox(t)[0].Payload.Labels["service"])
}

// A rule whose SQL fails to run records the error, like any source.
func Test_Evaluate_LogsSQLRuleThatFails_RecordsTheError(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "", 0, 0)
	rule.Source, rule.Query = SourceLogsSQL, RuleQuery{SQL: "SELECT no_such_column AS value FROM logs"}
	require.NoError(t, f.repo.saveRule(rule))

	require.NoError(t, f.evaluator.Evaluate(context.Background(), rule))

	stored, err := f.repo.rule(f.projectID, rule.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.LastError, "no_such_column")
}
