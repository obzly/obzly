package correlation_api

import (
	"net/http"
	"strconv"
	"time"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CorrelationController serves incident sessions under "/correlation".
type CorrelationController struct {
	service *CorrelationService
}

func (c *CorrelationController) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/correlation/sessions/:projectId", c.ListSessions)
	router.GET("/correlation/sessions/:projectId/:sessionId", c.GetSessionDetail)
}

// ListSessions
// @Summary List incident sessions
// @Description Lists dimensionally+temporally correlated incident sessions for a project (not semantic/ML-based correlation - see correlation_core's package doc comment)
// @Tags correlation
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string false "Range start, RFC3339"
// @Param to query string false "Range end, RFC3339"
// @Param limit query int false "Max sessions returned"
// @Success 200 {object} correlation_core.ListSessionsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /correlation/sessions/{projectId} [get]
func (c *CorrelationController) ListSessions(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	from, to := parseTimeRangeQuery(ctx)
	limit := 0
	if limitStr := ctx.Query("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}

	response, err := c.service.ListSessions(projectID, from, to, limit, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetSessionDetail
// @Summary Get one incident session's re-hydrated detail
// @Description Re-hydrates every signal belonging to a session against its live owning stream (logs/spans) - a signal whose underlying row no longer exists (e.g. retention deleted it) is marked unavailable rather than failing the whole response
// @Tags correlation
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param sessionId path string true "Session ID"
// @Success 200 {object} correlation_core.SessionDetailDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /correlation/sessions/{projectId}/{sessionId} [get]
func (c *CorrelationController) GetSessionDetail(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	sessionID, err := uuid.Parse(ctx.Param("sessionId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid session ID"})
		return
	}

	detail, err := c.service.GetSessionDetail(projectID, sessionID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, detail)
}

func parseTimeRangeQuery(ctx *gin.Context) (*time.Time, *time.Time) {
	var from, to *time.Time
	if fromStr := ctx.Query("from"); fromStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, fromStr); err == nil {
			from = &parsed
		}
	}
	if toStr := ctx.Query("to"); toStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, toStr); err == nil {
			to = &parsed
		}
	}
	return from, to
}

func (c *CorrelationController) handleError(ctx *gin.Context, err error) {
	switch err.Error() {
	case "insufficient permissions to query project correlation sessions":
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
