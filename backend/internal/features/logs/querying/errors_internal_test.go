package logs_querying

import (
	"fmt"
	"net/http"
	"testing"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/require"
)

func Test_ErrorResponse_MemoryErrors_AnswerRetryOrNarrowNotServerError(t *testing.T) {
	controller := &LogQueryController{}

	busy, body := controller.errorResponse(segments.ErrMemoryBusy)
	require.Equal(t, http.StatusTooManyRequests, busy)
	require.Equal(t, logs_core.ErrorTooManyConcurrentQueries, body["code"])

	outOfMemory, body := controller.errorResponse(fmt.Errorf("%w: Out of Memory Error", logs_core.ErrSQLOutOfMemory))
	require.Equal(t, http.StatusUnprocessableEntity, outOfMemory)
	require.Equal(t, "QUERY_NEEDS_MORE_MEMORY", body["code"])

	invalid, _ := controller.errorResponse(fmt.Errorf("invalid query: Binder Error: no such column"))
	require.Equal(t, http.StatusBadRequest, invalid)
}
