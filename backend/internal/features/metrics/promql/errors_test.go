package metrics_promql_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A storage failure under a query - pushed down when the query is built, or
// read by the engine while it evaluates - has no text for the caller: it
// reached them as "invalid PromQL: query fragment failed: ... executor ...
// /var/lib/...parquet".
func Test_ClientMessage_StorageFailure_HasNoTextForTheCaller(t *testing.T) {
	metrics_testsupport.FailSampleReads(t, nil)
	end := time.Now().UTC()

	for _, expression := range []string{"sum(rate(http_requests_total[5m]))", "http_requests_total"} {
		err := runRangeQuery(expression, end)

		require.ErrorContains(t, err, "executor querier-0", expression)
		message, ok := metrics_promql.ClientMessage(err)
		require.False(t, ok, expression)
		require.Empty(t, message, expression)
	}
}

// What the expression causes keeps its text: a parse error, an evaluation
// error, and a storage limit (without what wraps it on the way up).
func Test_ClientMessage_MistakeInTheQuery_KeepsItsText(t *testing.T) {
	limit := &metrics_core.LimitError{Message: "query returns more than 10 points; narrow the time range or matchers"}
	metrics_testsupport.FailSampleReads(t, fmt.Errorf("failed to merge query fragments: %w", limit))
	end := time.Now().UTC()

	message, ok := metrics_promql.ClientMessage(runRangeQuery("rate(http_requests_total[5m", end))
	require.True(t, ok)
	require.Contains(t, message, "parse error")

	for _, expression := range []string{"sum(rate(http_requests_total[5m]))", "http_requests_total"} {
		message, ok = metrics_promql.ClientMessage(runRangeQuery(expression, end))
		require.True(t, ok, expression)
		require.Equal(t, limit.Message, message, expression)
	}

	message, ok = metrics_promql.ClientMessage(errors.New("many-to-many matching not allowed"))
	require.True(t, ok)
	require.Equal(t, "many-to-many matching not allowed", message)
}

// runRangeQuery builds and evaluates expression over the 10 minutes before
// end and returns the error of whichever failed.
func runRangeQuery(expression string, end time.Time) error {
	query, err := metrics_promql.NewRangeQuery(
		context.Background(), uuid.New(), expression, end.Add(-10*time.Minute), end, time.Minute)
	if err != nil {
		return err
	}
	defer query.Close()
	return query.Exec(context.Background()).Err
}
