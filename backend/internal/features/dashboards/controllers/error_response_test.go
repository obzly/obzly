package dashboards_controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	dashboards_services "obzly/internal/features/dashboards/services"
	"obzly/internal/query"
	"obzly/internal/util/querylimit"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A panel query out of time is a 408 asking for a narrower range, not a 400
// carrying the engine's or a fragment's error text.
func Test_PanelQueryError_Timeout_Is408(t *testing.T) {
	controller := &DashboardController{}

	status, body := controller.errorResponse(
		fmt.Errorf("invalid PromQL query: query fragment failed: executor e1: %w", context.DeadlineExceeded))
	require.Equal(t, http.StatusRequestTimeout, status)
	require.NotContains(t, body["error"], "executor")

	status, _ = controller.errorResponse(errors.New("invalid PromQL query: parse error"))
	require.Equal(t, http.StatusBadRequest, status)
}

// A panel query that failed in storage answers 500 with the sentinel's text:
// the default branch returned the whole chain, with the fragment, the
// executor and the segment file in it. What is in the chain for a reason
// (a scan budget refusal, a full limiter) keeps its own answer.
func Test_PanelQueryError_StorageFailure_Is500WithoutItsCause(t *testing.T) {
	controller := &DashboardController{}
	failure := fmt.Errorf("%w: %w", dashboards_services.ErrPanelQueryFailed, errors.New(
		`query fragment failed: executor querier-0: Cannot open file "/var/lib/obzly/segments/spans/1.parquet"`))

	status, body := controller.errorResponse(failure)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, gin.H{"error": "failed to execute panel query"}, body)

	status, body = controller.errorResponse(fmt.Errorf("%w: %w", dashboards_services.ErrPanelQueryFailed,
		&query.ScanBudgetError{Planned: 2 << 30, Budget: 1 << 30}))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "QUERY_TOO_LARGE", body["code"])

	status, _ = controller.errorResponse(
		fmt.Errorf("%w: %w", dashboards_services.ErrPanelQueryFailed, querylimit.ErrTooManyQueries))
	require.Equal(t, http.StatusTooManyRequests, status)
}
