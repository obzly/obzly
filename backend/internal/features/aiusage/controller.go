package aiusage

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AIUsageController struct {
	service *AIUsageService
}

func (c *AIUsageController) RegisterRoutes(router *gin.RouterGroup) {
	routes := router.Group("/ai-usage")

	routes.GET("/overview/:projectId", c.GetOverview)
	routes.GET("/sessions/:projectId", c.GetSessions)
	routes.GET("/sessions/:projectId/:sessionId/events", c.GetSessionEvents)
	routes.GET("/events/:projectId", c.GetEvents)
	routes.GET("/llm/overview/:projectId", c.GetLLMOverview)
	routes.GET("/llm/calls/:projectId", c.GetLLMCalls)
}

// GetLLMOverview
// @Summary LLM apps overview
// @Description Calls, tokens, errors, latency and estimated cost of OpenTelemetry gen_ai spans, by service, model, provider or operation. Never returns message content.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Param groupBy query string false "Breakdown: service (default), model, provider or operation"
// @Param bucketSeconds query int false "Timeseries bucket size; picked from the range when omitted"
// @Success 200 {object} aiusage.LLMOverviewResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/llm/overview/{projectId} [get]
func (c *AIUsageController) GetLLMOverview(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}
	bucketSeconds, err := optionalInt(ctx, "bucketSeconds", 0)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bucketSeconds"})
		return
	}

	response, err := c.service.GetLLMOverview(ctx.Request.Context(), projectID, &LLMOverviewRequest{
		From: filter.From, To: filter.To, GroupBy: ctx.Query("groupBy"), BucketSeconds: int64(bucketSeconds),
	}, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// GetLLMCalls
// @Summary Slowest and failed LLM calls
// @Description The slowest calls and the latest failed calls in the range, with their trace IDs. Never returns message content.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Param limit query int false "Calls per list, 1-100 (default 20)"
// @Success 200 {object} aiusage.LLMCallsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/llm/calls/{projectId} [get]
func (c *AIUsageController) GetLLMCalls(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}
	limit, err := optionalInt(ctx, "limit", defaultLLMCalls)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit"})
		return
	}

	response, err := c.service.GetLLMCalls(ctx.Request.Context(), projectID, &filter, limit, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// GetOverview
// @Summary Claude Code usage overview
// @Description Cost, tokens, sessions, tools and errors from Claude Code's OpenTelemetry events. Cost is Claude Code's API-equivalent estimate.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Param user query string false "Only this user_email"
// @Param model query string false "Only this model"
// @Param terminal query string false "Only this terminal_type"
// @Param bucketSeconds query int false "Timeseries bucket size; picked from the range when omitted"
// @Param groupBy query string false "Timeseries split: model (default) or user"
// @Success 200 {object} aiusage.OverviewResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/overview/{projectId} [get]
func (c *AIUsageController) GetOverview(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}

	bucketSeconds, err := optionalInt(ctx, "bucketSeconds", 0)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bucketSeconds"})
		return
	}

	response, err := c.service.GetOverview(projectID, &OverviewRequest{
		UsageFilter:   filter,
		BucketSeconds: int64(bucketSeconds),
		GroupBy:       ctx.Query("groupBy"),
	}, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetSessions
// @Summary Claude Code sessions
// @Description One row per Claude Code session in the range, newest first by default.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Param user query string false "Only this user_email"
// @Param model query string false "Only this model"
// @Param terminal query string false "Only this terminal_type"
// @Param search query string false "Session ID or user contains"
// @Param sortBy query string false "lastSeen (default), cost, requests or tokens"
// @Param limit query int false "Page size, 1-200 (default 20)"
// @Param offset query int false "Rows to skip"
// @Success 200 {object} aiusage.SessionsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/sessions/{projectId} [get]
func (c *AIUsageController) GetSessions(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}

	limit, err := optionalInt(ctx, "limit", 20)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit"})
		return
	}
	offset, err := optionalInt(ctx, "offset", 0)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset"})
		return
	}

	response, err := c.service.GetSessions(projectID, &SessionsRequest{
		UsageFilter: filter,
		Search:      ctx.Query("search"),
		SortBy:      ctx.Query("sortBy"),
		Limit:       limit,
		Offset:      offset,
	}, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetEvents
// @Summary Claude Code events
// @Description Raw Claude Code events in the range, newest first, with their fields.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Param user query string false "Only this user_email"
// @Param model query string false "Only this model"
// @Param terminal query string false "Only this terminal_type"
// @Param event query string false "Event name without the claude_code. prefix, e.g. api_request"
// @Param sessionId query string false "Only this session"
// @Param search query string false "Case-insensitive match anywhere in the fields"
// @Param limit query int false "Page size, 1-200 (default 50)"
// @Param offset query int false "Rows to skip"
// @Success 200 {object} aiusage.EventsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/events/{projectId} [get]
func (c *AIUsageController) GetEvents(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}

	limit, err := optionalInt(ctx, "limit", 50)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit"})
		return
	}
	offset, err := optionalInt(ctx, "offset", 0)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset"})
		return
	}

	response, err := c.service.GetEvents(projectID, &EventsRequest{
		UsageFilter: filter,
		Event:       ctx.Query("event"),
		SessionID:   ctx.Query("sessionId"),
		Search:      ctx.Query("search"),
		Limit:       limit,
		Offset:      offset,
	}, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetSessionEvents
// @Summary Claude Code session events
// @Description The session's events in time order (up to 500): prompts, API requests, tool decisions and results, errors.
// @Tags ai-usage
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param sessionId path string true "Claude Code session ID"
// @Param from query string true "Range start, RFC3339"
// @Param to query string true "Range end, RFC3339"
// @Success 200 {object} aiusage.SessionEventsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ai-usage/sessions/{projectId}/{sessionId}/events [get]
func (c *AIUsageController) GetSessionEvents(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, filter, ok := parseCommon(ctx)
	if !ok {
		return
	}

	response, err := c.service.GetSessionEvents(projectID, ctx.Param("sessionId"), &filter, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

func (c *AIUsageController) handleError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrInvalidRequest):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query AI usage"})
	}
}

func parseCommon(ctx *gin.Context) (uuid.UUID, UsageFilter, bool) {
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return uuid.Nil, UsageFilter{}, false
	}

	from, fromErr := time.Parse(time.RFC3339Nano, ctx.Query("from"))
	to, toErr := time.Parse(time.RFC3339Nano, ctx.Query("to"))
	if fromErr != nil || toErr != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "from and to must be RFC3339 timestamps"})
		return uuid.Nil, UsageFilter{}, false
	}

	return projectID, UsageFilter{
		From:     from.UTC(),
		To:       to.UTC(),
		User:     ctx.Query("user"),
		Model:    ctx.Query("model"),
		Terminal: ctx.Query("terminal"),
	}, true
}

func optionalInt(ctx *gin.Context, name string, fallback int) (int, error) {
	raw := ctx.Query(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
