package logs_querier_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Against the previous hour: a pattern that holds is not listed; one that
// first shows up is new, one that quadruples spikes, one that falls away
// drops - each with its template, newest log and level.
func Test_ExecutePatternChanges_NewSpikingAndDroppingPatterns(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	var items []*logs_core.LogItem
	add := func(hour int, n int, level logs_core.LogLevel, format string) {
		for i := range n {
			items = append(items, &logs_core.LogItem{
				Timestamp: base.Add(time.Duration(hour)*time.Hour + time.Duration(i)*time.Minute),
				Level:     level, Message: fmt.Sprintf(format, i),
			})
		}
	}
	add(0, 20, logs_core.LogLevelInfo, "request %d served")
	add(1, 20, logs_core.LogLevelInfo, "request %d served")
	add(0, 10, logs_core.LogLevelInfo, "cache %d warmed")
	add(1, 2, logs_core.LogLevelInfo, "cache %d warmed")
	add(1, 8, logs_core.LogLevelError, "payment %d failed: upstream 301")
	add(0, 5, logs_core.LogLevelWarn, "retry %d scheduled")
	add(1, 20, logs_core.LogLevelWarn, "retry %d scheduled")
	projectID, querier := seedProject(t, items)

	baseline := &logs_core.TimeRangeDTO{From: &base, To: new(base.Add(time.Hour - time.Nanosecond))}
	current := &logs_core.TimeRangeDTO{From: new(base.Add(time.Hour)), To: new(base.Add(2*time.Hour - time.Nanosecond))}
	response, err := querier.ExecutePatternChanges(context.Background(), projectID, &logs_core.PatternChangesRequestDTO{
		Current: current, Baseline: baseline,
	})
	require.NoError(t, err)

	type summary struct {
		pattern, level, change string
		current, baseline      int64
	}
	var got []summary
	for _, change := range response.Changes {
		got = append(got, summary{change.Pattern, change.Level, change.Change, change.Current, change.Baseline})
		assert.Equal(t, logs_core.PatternHash(change.Sample), change.Hash)
	}
	assert.Equal(t, []summary{
		{"payment <num> failed: upstream <num>", "error", logs_core.PatternChangeNew, 8, 0},
		{"retry <num> scheduled", "warn", logs_core.PatternChangeSpike, 20, 5},
		{"cache <num> warmed", "info", logs_core.PatternChangeDrop, 2, 10},
	}, got)
	assert.Equal(t, int64(50), response.CurrentTotal)
	assert.Equal(t, int64(35), response.BaselineTotal)

	errorsOnly, err := querier.ExecutePatternChanges(context.Background(), projectID, &logs_core.PatternChangesRequestDTO{
		Query:   condition("level", logs_core.ConditionOperatorEquals, "error"),
		Current: current, Baseline: baseline,
	})
	require.NoError(t, err)
	require.Len(t, errorsOnly.Changes, 1)
	assert.Equal(t, logs_core.PatternChangeNew, errorsOnly.Changes[0].Change)
}
