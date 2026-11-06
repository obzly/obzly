package dashboards_controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_services "obzly/internal/features/dashboards/services"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/query"
	"obzly/internal/util/querylimit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type DashboardController struct {
	dashboardService *dashboards_services.DashboardService
}

func (c *DashboardController) RegisterRoutes(router *gin.RouterGroup) {
	dashboardRoutes := router.Group("/projects/dashboards/:projectId")

	dashboardRoutes.POST("", c.CreateDashboard)
	dashboardRoutes.GET("", c.ListDashboards)
	dashboardRoutes.POST("/query", c.ExecutePanelQuery)
	dashboardRoutes.POST("/query/stream", c.StreamPanelQuery)
	dashboardRoutes.POST("/overview", c.GenerateOverview)
	dashboardRoutes.POST("/import", c.ImportDashboard)
	dashboardRoutes.GET("/:dashboardId", c.GetDashboard)
	dashboardRoutes.PUT("/:dashboardId", c.UpdateDashboard)
	dashboardRoutes.PUT("/:dashboardId/pin", c.SetPinned)
	dashboardRoutes.PUT("/:dashboardId/folder", c.SetDashboardFolder)
	dashboardRoutes.DELETE("/:dashboardId", c.DeleteDashboard)

	folderRoutes := router.Group("/projects/dashboards/:projectId/folders")
	folderRoutes.POST("", c.CreateFolder)
	folderRoutes.GET("", c.ListFolders)
	folderRoutes.PUT("/:folderId", c.RenameFolder)
	folderRoutes.DELETE("/:folderId", c.DeleteFolder)
}

// CreateFolder
// @Summary Create a dashboard folder
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body dashboards_dto.CreateFolderRequestDTO true "Folder title"
// @Success 200 {object} dashboards_models.Folder
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/folders [post]
func (c *DashboardController) CreateFolder(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.CreateFolderRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	folder, err := c.dashboardService.CreateFolder(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, folder)
}

// ListFolders
// @Summary List a project's dashboard folders
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} dashboards_dto.ListFoldersResponseDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/folders [get]
func (c *DashboardController) ListFolders(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	response, err := c.dashboardService.ListFolders(projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// RenameFolder
// @Summary Rename a dashboard folder
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param folderId path string true "Folder ID"
// @Param request body dashboards_dto.RenameFolderRequestDTO true "New title"
// @Success 200 {object} dashboards_models.Folder
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/folders/{folderId} [put]
func (c *DashboardController) RenameFolder(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}
	folderID, err := uuid.Parse(ctx.Param("folderId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid folder ID"})
		return
	}

	var request dashboards_dto.RenameFolderRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	folder, err := c.dashboardService.RenameFolder(projectID, folderID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, folder)
}

// DeleteFolder
// @Summary Delete a dashboard folder (its dashboards become unfiled, not deleted)
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param folderId path string true "Folder ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/folders/{folderId} [delete]
func (c *DashboardController) DeleteFolder(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}
	folderID, err := uuid.Parse(ctx.Param("folderId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid folder ID"})
		return
	}

	if err := c.dashboardService.DeleteFolder(projectID, folderID, user); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Folder deleted successfully"})
}

// SetDashboardFolder
// @Summary Move a dashboard into a folder, or un-file it (folderId: null)
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param dashboardId path string true "Dashboard ID"
// @Param request body dashboards_dto.SetDashboardFolderRequestDTO true "Target folder ID, or null to un-file"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/{dashboardId}/folder [put]
func (c *DashboardController) SetDashboardFolder(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, dashboardID, ok := parseProjectAndDashboardID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.SetDashboardFolderRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	dashboard, err := c.dashboardService.SetDashboardFolder(projectID, dashboardID, request.FolderID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// SetPinned
// @Summary Pin or unpin a dashboard
// @Description Pinned dashboards sort to the top of the project's dashboard list
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param dashboardId path string true "Dashboard ID"
// @Param request body dashboards_dto.SetPinnedRequestDTO true "Pinned state"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/{dashboardId}/pin [put]
func (c *DashboardController) SetPinned(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, dashboardID, ok := parseProjectAndDashboardID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.SetPinnedRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	dashboard, err := c.dashboardService.SetPinned(projectID, dashboardID, request.Pinned, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// CreateDashboard
// @Summary Create a dashboard
// @Description Create a new dashboard with an initial set of panels for a project
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body dashboards_dto.CreateDashboardRequestDTO true "Dashboard data"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId} [post]
func (c *DashboardController) CreateDashboard(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.CreateDashboardRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	dashboard, err := c.dashboardService.CreateDashboard(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// ListDashboards
// @Summary List a project's dashboards
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} dashboards_dto.ListDashboardsResponseDTO
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId} [get]
func (c *DashboardController) ListDashboards(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	response, err := c.dashboardService.ListDashboards(projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetDashboard
// @Summary Get a dashboard
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param dashboardId path string true "Dashboard ID"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/{dashboardId} [get]
func (c *DashboardController) GetDashboard(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, dashboardID, ok := parseProjectAndDashboardID(ctx)
	if !ok {
		return
	}

	dashboard, err := c.dashboardService.GetDashboard(projectID, dashboardID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// UpdateDashboard
// @Summary Update a dashboard
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param dashboardId path string true "Dashboard ID"
// @Param request body dashboards_dto.UpdateDashboardRequestDTO true "Dashboard data"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/{dashboardId} [put]
func (c *DashboardController) UpdateDashboard(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, dashboardID, ok := parseProjectAndDashboardID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.UpdateDashboardRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	dashboard, err := c.dashboardService.UpdateDashboard(projectID, dashboardID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// DeleteDashboard
// @Summary Delete a dashboard
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param dashboardId path string true "Dashboard ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/{dashboardId} [delete]
func (c *DashboardController) DeleteDashboard(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, dashboardID, ok := parseProjectAndDashboardID(ctx)
	if !ok {
		return
	}

	if err := c.dashboardService.DeleteDashboard(projectID, dashboardID, user); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Dashboard deleted successfully"})
}

// ExecutePanelQuery
// @Summary Run one panel's aggregation query
// @Description Executes a panel definition (saved or still being edited) against the project's logs and returns chart-ready buckets
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body dashboards_dto.PanelQueryRequestDTO true "Panel definition and time range"
// @Success 200 {object} dashboards_dto.PanelQueryResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /projects/dashboards/{projectId}/query [post]
func (c *DashboardController) ExecutePanelQuery(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.PanelQueryRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.dashboardService.ExecutePanelQuery(ctx.Request.Context(), projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// panelHeartbeatInterval: a streamed panel query sends a heartbeat frame
// this often while a partition group runs, so the browser and proxies
// know it's alive.
const panelHeartbeatInterval = 10 * time.Second

// StreamPanelQuery
// @Summary Run one panel's aggregation query, streamed
// @Description Same request as /query; the answer is newline-delimited JSON frames. A time-bucketed logs panel is read partition group by partition group, newest first, and each "buckets" frame's buckets add to the earlier ones' (the same bucket and group can come again); any other panel is one "buckets" frame. Then "done", or "error" with status. "heartbeat" frames keep the connection alive. A refused query (403, 400) is a plain JSON error before any frame.
// @Tags dashboards
// @Accept json
// @Produce application/x-ndjson
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body dashboards_dto.PanelQueryRequestDTO true "Panel query request"
// @Success 200 {object} dashboards_dto.PanelFrameDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /projects/dashboards/{projectId}/query/stream [post]
func (c *DashboardController) StreamPanelQuery(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}
	var request dashboards_dto.PanelQueryRequestDTO
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
	writeFrame := func(frame dashboards_dto.PanelFrameDTO) error {
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
		heartbeat.Go(func() {
			ticker := time.NewTicker(panelHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopHeartbeat:
					return
				case <-ticker.C:
					if writeFrame(dashboards_dto.PanelFrameDTO{Type: dashboards_dto.PanelFrameHeartbeat}) != nil {
						return
					}
				}
			}
		})
	}
	emit := func(buckets []dashboards_dto.PanelBucketDTO) error {
		return writeFrame(dashboards_dto.PanelFrameDTO{Type: dashboards_dto.PanelFrameBuckets, Buckets: buckets})
	}

	err := c.dashboardService.StreamPanelQuery(ctx.Request.Context(), projectID, &request, user, start, emit)
	if err != nil {
		if !started {
			c.handleError(ctx, err)
			return
		}
		status, body := c.errorResponse(err)
		message, _ := body["error"].(string)
		_ = writeFrame(
			dashboards_dto.PanelFrameDTO{Type: dashboards_dto.PanelFrameError, Status: status, Error: message},
		)
		return
	}
	_ = writeFrame(dashboards_dto.PanelFrameDTO{Type: dashboards_dto.PanelFrameDone})
}

// GenerateOverview
// @Summary Generate (or regenerate) the project's auto-populated Overview dashboard
// @Tags dashboards
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Success 200 {object} dashboards_models.Dashboard
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/overview [post]
func (c *DashboardController) GenerateOverview(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	dashboard, err := c.dashboardService.GenerateOverview(projectID, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, dashboard)
}

// ImportDashboard
// @Summary Import a Grafana or OpenObserve dashboard JSON export
// @Description Best-effort layout-level import (title, panel titles/types/grid position) - queries are not translated; see response Warnings
// @Tags dashboards
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param projectId path string true "Project ID"
// @Param request body dashboards_dto.ImportDashboardRequestDTO true "Format and raw dashboard JSON"
// @Success 200 {object} dashboards_dto.ImportDashboardResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/dashboards/{projectId}/import [post]
func (c *DashboardController) ImportDashboard(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	var request dashboards_dto.ImportDashboardRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.dashboardService.ImportDashboard(projectID, &request, user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

func parseProjectID(ctx *gin.Context) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return uuid.Nil, false
	}
	return projectID, true
}

func parseProjectAndDashboardID(ctx *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}

	dashboardID, err := uuid.Parse(ctx.Param("dashboardId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid dashboard ID"})
		return uuid.Nil, uuid.Nil, false
	}

	return projectID, dashboardID, true
}

func (c *DashboardController) handleError(ctx *gin.Context, err error) {
	ctx.JSON(c.errorResponse(err))
}

// errorResponse is the HTTP status and body for a dashboards error.
func (c *DashboardController) errorResponse(err error) (int, gin.H) {
	if message, refused := query.BudgetRefusal(err); refused {
		return http.StatusUnprocessableEntity, gin.H{"error": message, "code": "QUERY_TOO_LARGE"}
	}
	if errors.Is(err, querylimit.ErrTooManyQueries) {
		return http.StatusTooManyRequests, gin.H{"error": err.Error()}
	}
	switch err.Error() {
	case "insufficient permissions to access project dashboards",
		"insufficient permissions to modify project dashboards":
		return http.StatusForbidden, gin.H{"error": err.Error()}
	case "dashboard not found", "dashboard folder not found":
		return http.StatusNotFound, gin.H{"error": err.Error()}
	default:
		return http.StatusBadRequest, gin.H{"error": err.Error()}
	}
}
