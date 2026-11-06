package logs_querying

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/query"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// searchHeartbeatInterval: how often a streamed search proves it is alive.
const searchHeartbeatInterval = 10 * time.Second

type LogQueryController struct {
	logQueryService *LogQueryService
}

func (c *LogQueryController) RegisterRoutes(router *gin.RouterGroup) {
	queryRoutes := router.Group("/logs/query")

	queryRoutes.POST("/execute/:projectId", c.ExecuteQuery)
	queryRoutes.POST("/stream/:projectId", c.StreamQuery)
	queryRoutes.POST("/execute-sql/:projectId", c.ExecuteSQLQuery)
	queryRoutes.POST("/patterns/:projectId", c.ExecutePatterns)
	queryRoutes.POST("/field-values/:projectId", c.ExecuteFieldValues)
	queryRoutes.POST("/context/:projectId", c.ExecuteContext)
	queryRoutes.POST("/deploys/:projectId", c.ExecuteDeploys)
	queryRoutes.POST("/changes/:projectId", c.ExecutePatternChanges)
	queryRoutes.GET("/fields/:projectId", c.GetQueryableFields)
	queryRoutes.GET("/stats/:projectId", c.GetProjectStats)
	queryRoutes.GET("/system-stats", c.GetSystemStats)
	queryRoutes.GET("/system-dashboard", c.GetSystemDashboard)
}

// ExecuteQuery
// @Summary Execute log query
// @Description Execute a structured query against project logs. timeRange.to is required for pagination consistency.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.LogQueryRequestDTO true "Query request"
// @Success 200 {object} logs_core.LogQueryResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 408 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/execute/{projectId} [post]
func (c *LogQueryController) ExecuteQuery(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectIDStr := ctx.Param("projectId")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.LogQueryRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.logQueryService.ExecuteQuery(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// StreamQuery
// @Summary Stream a log query's results window by window
// @Description Same request as execute. The search reads its time range newest-first (oldest-first for sortOrder asc) in growing windows and stops once the page is full; the response is NDJSON: a "rows" frame per window with the rows it added, a "progress" frame for a window without any, then "done" (nextCursor, total, executedIn) or "error". Access, validation and concurrency errors come back as plain JSON before any frame.
// @Tags logs-query
// @Accept json
// @Produce application/x-ndjson
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.LogQueryRequestDTO true "Query request"
// @Success 200 {object} SearchFrameDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/stream/{projectId} [post]
func (c *LogQueryController) StreamQuery(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}
	var request logs_core.LogQueryRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	flusher, canFlush := ctx.Writer.(http.Flusher)
	if !canFlush {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming not supported"})
		return
	}

	started := false
	var writeMu sync.Mutex
	writeFrame := func(frame SearchFrameDTO) error {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := ctx.Writer.Write(append(encoded, '\n')); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	stopHeartbeat := make(chan struct{})
	var heartbeat sync.WaitGroup
	// Stop the heartbeat before returning: gin's writer is done with then.
	defer heartbeat.Wait()
	defer close(stopHeartbeat)
	start := func() {
		started = true
		ctx.Writer.Header().Set("Content-Type", "application/x-ndjson")
		ctx.Writer.Header().Set("Cache-Control", "no-cache")
		ctx.Writer.Header().Set("X-Accel-Buffering", "no")
		ctx.Writer.WriteHeader(http.StatusOK)
		flusher.Flush()
		// A window can take minutes without a row: heartbeats tell the
		// browser (and proxies) the search is alive.
		heartbeat.Go(func() {
			ticker := time.NewTicker(searchHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopHeartbeat:
					return
				case <-ticker.C:
					if writeFrame(SearchFrameDTO{Type: SearchFrameHeartbeat}) != nil {
						return
					}
				}
			}
		})
	}
	emit := func(logs []logs_core.LogItemDTO, window logs_core.SearchWindowDTO) error {
		frame := SearchFrameDTO{Type: SearchFrameRows, Logs: logs, Window: &window}
		if len(logs) == 0 {
			frame = SearchFrameDTO{Type: SearchFrameProgress, Window: &window}
		}
		return writeFrame(frame)
	}

	response, err := c.logQueryService.StreamQuery(ctx.Request.Context(), projectID, &request, user, start, emit)
	if err != nil {
		status, body := c.errorResponse(err)
		if !started {
			ctx.JSON(status, body)
			return
		}
		message, _ := body["error"].(string)
		_ = writeFrame(SearchFrameDTO{Type: SearchFrameError, Status: status, Error: message})
		return
	}
	_ = writeFrame(SearchFrameDTO{
		Type: SearchFrameDone, NextCursor: response.NextCursor, Total: &response.Total,
		CountSkipped: response.CountSkipped, ExecutedInMs: response.ExecutedInMs,
		Partial: response.Partial, SearchedThrough: response.SearchedThrough,
	})
}

// ExecutePatterns
// @Summary List the most frequent log message patterns
// @Description Groups the logs a structured query matches by message template (UUIDs, timestamps, IPs, hex ids and numbers masked) and returns the most frequent ones with their pattern.hash - filter on that hash to see one pattern's logs.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.PatternsRequestDTO true "Patterns request"
// @Success 200 {object} logs_core.PatternsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/patterns/{projectId} [post]
func (c *LogQueryController) ExecutePatterns(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.PatternsRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.logQueryService.ExecutePatterns(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ExecuteFieldValues
// @Summary List a field's most frequent values
// @Description Counts the values of one field (system or custom) among the logs a structured query matches, most frequent first. With sampleRows the range is read from its newest end and the count stops once that many matching logs were counted; sampledFrom then says where the sample starts. With includeRecent, a log source field (service, namespace, workload, container, pod, node, cluster, host) also lists in recent the values whose sources logged in the last 36 hours or in the range but are not in values, newest first and without counts; recentSkipped is set when the query filters on other fields.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.FieldValuesRequestDTO true "Field values request"
// @Success 200 {object} logs_core.FieldValuesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/field-values/{projectId} [post]
func (c *LogQueryController) ExecuteFieldValues(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.FieldValuesRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.logQueryService.ExecuteFieldValues(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ExecutePatternChanges
// @Summary Compare message patterns between two time ranges ("what changed?")
// @Description Each message pattern's count in current against baseline, scaled to current's length: patterns new in current, spiking (at least twice their baseline) or dropping (at most half), with each one's template and newest log. Counts read per-minute counts where segments have them.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.PatternChangesRequestDTO true "Pattern changes request"
// @Success 200 {object} logs_core.PatternChangesResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/changes/{projectId} [post]
func (c *LogQueryController) ExecutePatternChanges(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}
	var request logs_core.PatternChangesRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	response, err := c.logQueryService.ExecutePatternChanges(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// ExecuteDeploys
// @Summary List the Deployment rollouts in a time range
// @Description Deploy markers for the Logs histogram: each new ReplicaSet whose pods first logged inside the range (not at its start), from the pods' names - a Deployment's pods are <deployment>-<hash>-<suffix>. Query narrows them when it filters only on level, service, namespace, container, pod or pattern.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.DeploysRequestDTO true "Deploys request"
// @Success 200 {object} logs_core.DeploysResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/deploys/{projectId} [post]
func (c *LogQueryController) ExecuteDeploys(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}
	var request logs_core.DeploysRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	response, err := c.logQueryService.ExecuteDeploys(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// ExecuteContext
// @Summary Show the logs around one log
// @Description Returns the logs just before and after one log (the anchor, by id and timestamp) from the same source, whatever search found it - what led up to an error is usually filtered out of the search. Scope lists the fields whose anchor values the context shares (omitted: the default source fields such as k8s_pod_name and host_name; []: the whole project). Direction and cursor load more of one side.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.LogContextRequestDTO true "Log context request"
// @Success 200 {object} logs_core.LogContextResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/context/{projectId} [post]
func (c *LogQueryController) ExecuteContext(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.LogContextRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.logQueryService.ExecuteContext(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ExecuteSQLQuery
// @Summary Execute a raw SQL query against project logs ("SQL mode")
// @Description Runs a user-written single SELECT statement against a "logs" table scoped to this project and time range - see logs_core.ExecuteSQLQueryRequestDTO's doc comment for the query surface (columns: timestamp, level, message, client_ip, trace_id, span_id, fields) and segments.ValidateSingleStatementSelect for the security model.
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body logs_core.ExecuteSQLQueryRequestDTO true "SQL query request"
// @Success 200 {object} logs_core.SQLQueryResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 408 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/query/execute-sql/{projectId} [post]
func (c *LogQueryController) ExecuteSQLQuery(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.ExecuteSQLQueryRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.logQueryService.ExecuteSQLQuery(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetQueryableFields
// @Summary Get available queryable fields
// @Description Get list of fields that can be queried for a project, with optional search query
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param query query string false "Search query to filter field names"
// @Success 200 {object} logs_core.GetQueryableFieldsResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/fields/{projectId} [get]
func (c *LogQueryController) GetQueryableFields(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectIDStr := ctx.Param("projectId")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	var request logs_core.GetQueryableFieldsRequestDTO
	if err := ctx.ShouldBindQuery(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid query parameters"})
		return
	}

	response, err := c.logQueryService.GetQueryableFields(projectID, &request, user)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient permissions") {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get queryable fields"})
		}
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetProjectStats
// @Summary Get project log statistics
// @Description Get statistics about logs for a project including total count, size, and time range
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Success 200 {object} logs_core.LogsStatsDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/stats/{projectId} [get]
func (c *LogQueryController) GetProjectStats(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	projectIDStr := ctx.Param("projectId")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID format"})
		return
	}

	response, err := c.logQueryService.GetProjectStats(projectID, user)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient permissions") {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get project stats"})
		}
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetSystemStats
// @Summary Get system-wide log statistics (ADMIN only)
// @Description Get statistics about logs across all projects in the system
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} logs_core.LogsStatsDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/system-stats [get]
func (c *LogQueryController) GetSystemStats(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	response, err := c.logQueryService.GetSystemStats(user)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient permissions") {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get system stats"})
		}
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetSystemDashboard
// @Summary Get system dashboard metrics (ADMIN only)
// @Description Log stats plus host CPU/memory/disk usage and recent ingestion rate
// @Tags logs-query
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} logs_core.SystemDashboardDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /logs/query/system-dashboard [get]
func (c *LogQueryController) GetSystemDashboard(ctx *gin.Context) {
	user, isOk := ctx.MustGet("user").(*users_models.User)
	if !isOk {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user type in context"})
		return
	}

	response, err := c.logQueryService.GetSystemDashboard(user)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient permissions") {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get system dashboard"})
		}
		return
	}

	ctx.JSON(http.StatusOK, response)
}

func (c *LogQueryController) handleError(ctx *gin.Context, err error) {
	status, body := c.errorResponse(err)
	ctx.JSON(status, body)
}

// errorResponse is the HTTP status and body for a query error.
func (c *LogQueryController) errorResponse(err error) (int, gin.H) {
	if message, refused := query.BudgetRefusal(err); refused {
		return http.StatusUnprocessableEntity, gin.H{"error": message, "code": "QUERY_TOO_LARGE"}
	}

	if validationErr, ok := err.(*ValidationError); ok {
		return c.getStatusCodeForQueryValidationError(validationErr.Code), gin.H{
			"error": validationErr.Message,
			"code":  validationErr.Code,
		}
	}

	if strings.Contains(err.Error(), "insufficient permissions") {
		return http.StatusForbidden, gin.H{"error": err.Error()}
	}

	if strings.Contains(err.Error(), "invalid query") {
		return http.StatusBadRequest, gin.H{"error": err.Error()}
	}

	if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "context deadline") {
		return http.StatusRequestTimeout, gin.H{"error": "Query execution timed out"}
	}

	return http.StatusInternalServerError, gin.H{"error": "Failed to execute query"}
}

func (c *LogQueryController) getStatusCodeForQueryValidationError(errorCode string) int {
	switch errorCode {
	case logs_core.ErrorTooManyConcurrentQueries:
		return http.StatusTooManyRequests
	case logs_core.ErrorInvalidQueryStructure, logs_core.ErrorQueryTooComplex, logs_core.ErrorMissingTimeRangeTo:
		return http.StatusBadRequest
	case logs_core.ErrorQueryTimeout:
		return http.StatusRequestTimeout
	default:
		return http.StatusBadRequest
	}
}
