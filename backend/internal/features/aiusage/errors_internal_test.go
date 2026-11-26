package aiusage

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"obzly/internal/storage/segments"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The AI usage pages share SQL mode's sandbox: a replica short of memory
// answers 429 (retry), not 500.
func Test_HandleError_MemoryBusy_AnswersTooManyRequests(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	(&AIUsageController{}).handleError(ctx, segments.ErrMemoryBusy)

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
}
