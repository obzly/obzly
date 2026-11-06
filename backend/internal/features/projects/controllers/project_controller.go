package projects_controllers

import (
	"errors"
	"net/http"

	audit_logs "obzly/internal/features/audit_logs"
	projects_dto "obzly/internal/features/projects/dto"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ProjectController struct {
	projectService         *projects_services.ProjectService
	retentionPolicyService *projects_services.RetentionPolicyService
	promotedFieldService   *projects_services.PromotedFieldService
}

func (c *ProjectController) RegisterRoutes(router *gin.RouterGroup) {
	projectRoutes := router.Group("/projects")

	projectRoutes.POST("", c.CreateProject)
	projectRoutes.GET("", c.GetProjects)
	projectRoutes.GET("/:id", c.GetProject)
	projectRoutes.PUT("/:id", c.UpdateProject)
	projectRoutes.DELETE("/:id", c.DeleteProject)
	projectRoutes.GET("/:id/audit-logs", c.GetProjectAuditLogs)
	projectRoutes.GET("/:id/retention-policies", c.GetRetentionPolicies)
	projectRoutes.PUT("/:id/retention-policies", c.SetRetentionPolicies)
	projectRoutes.GET("/:id/promoted-fields", c.GetPromotedFields)
	projectRoutes.POST("/:id/promoted-fields", c.PromoteField)
	projectRoutes.DELETE("/:id/promoted-fields/:field", c.RetirePromotedField)
}

// PromoteFieldRequest names a log field to promote and its type.
type PromoteFieldRequest struct {
	Field string `json:"field" binding:"required"`
	Type  string `json:"type"  binding:"required"`
}

// GetPromotedFields
// @Summary List a project's promoted log fields
// @Description Log fields copied into typed columns at ingest, for faster filters and grouping
// @Tags projects
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} projects_models.PromotedField
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/promoted-fields [get]
func (c *ProjectController) GetPromotedFields(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}
	fields, err := c.promotedFieldService.ListPromotedFields(projectID, user)
	if err != nil {
		promotedFieldError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, fields)
}

// PromoteField
// @Summary Promote a log field
// @Description Copy a log field into a typed column at ingest (project owners and admins). Logs already stored keep being read from the fields JSON.
// @Tags projects
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body PromoteFieldRequest true "Field and type (string or number)"
// @Success 200 {object} projects_models.PromotedField
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/promoted-fields [post]
func (c *ProjectController) PromoteField(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}
	var request PromoteFieldRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	promoted, err := c.promotedFieldService.PromoteField(projectID, request.Field, request.Type, user)
	if err != nil {
		promotedFieldError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, promoted)
}

// RetirePromotedField
// @Summary Stop promoting a log field
// @Description The field is read from the fields JSON again; its column is not reused (project owners and admins)
// @Tags projects
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param field path string true "Field name"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /projects/{id}/promoted-fields/{field} [delete]
func (c *ProjectController) RetirePromotedField(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}
	if err := c.promotedFieldService.RetireField(projectID, ctx.Param("field"), user); err != nil {
		promotedFieldError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Field is no longer promoted"})
}

func promotedFieldError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projects_services.ErrPromotedFieldForbidden),
		errors.Is(err, projects_services.ErrRetentionPolicyNotFound):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, projects_services.ErrPromotedFieldNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

// GetRetentionPolicies
// @Summary Get a project's retention policies
// @Description Retention and sampling per signal (logs, traces, metrics), defaults where none is set
// @Tags projects
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {array} projects_models.RetentionPolicy
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/retention-policies [get]
func (c *ProjectController) GetRetentionPolicies(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}
	policies, err := c.retentionPolicyService.GetRetentionPolicies(projectID, user)
	if err != nil {
		retentionPolicyError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, policies)
}

// SetRetentionPolicies
// @Summary Set a project's retention policies
// @Description Replace the given signals' retention and sampling (project owners and admins)
// @Tags projects
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body []projects_models.RetentionPolicy true "Policies, one per signal"
// @Success 200 {array} projects_models.RetentionPolicy
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/retention-policies [put]
func (c *ProjectController) SetRetentionPolicies(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	projectID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}
	var policies []projects_models.RetentionPolicy
	if err := ctx.ShouldBindJSON(&policies); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	saved, err := c.retentionPolicyService.SetRetentionPolicies(projectID, policies, user)
	if err != nil {
		retentionPolicyError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, saved)
}

func retentionPolicyError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projects_services.ErrRetentionPolicyForbidden),
		errors.Is(err, projects_services.ErrRetentionPolicyNotFound):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

// CreateProject
// @Summary Create a new project
// @Description Create a new project with default settings
// @Tags projects
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body projects_dto.CreateProjectRequestDTO true "Project creation data"
// @Success 200 {object} projects_dto.ProjectResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects [post]
func (c *ProjectController) CreateProject(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request projects_dto.CreateProjectRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.projectService.CreateProject(&request, user)
	if err != nil {
		if err.Error() == "insufficient permissions to create projects" ||
			errors.Is(err, projects_services.ErrPlanLimit) {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})

		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetProjects
// @Summary List user's projects
// @Description Get list of projects the user is a member of
// @Tags projects
// @Produce json
// @Security BearerAuth
// @Success 200 {object} projects_dto.ListProjectsResponseDTO
// @Failure 401 {object} map[string]string
// @Router /projects [get]
func (c *ProjectController) GetProjects(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	response, err := c.projectService.GetUserProjects(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve projects"})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// GetProject
// @Summary Get project details
// @Description Get detailed information about a specific project
// @Tags projects
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} projects_models.Project
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id} [get]
func (c *ProjectController) GetProject(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectIDStr := ctx.Param("id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	project, err := c.projectService.GetProject(projectID, user)
	if err != nil {
		if err.Error() == "insufficient permissions to view project" {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, project)
}

// UpdateProject
// @Summary Update project settings
// @Description Update project configuration and settings
// @Tags projects
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param request body projects_models.Project true "Project update data"
// @Success 200 {object} projects_models.Project
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id} [put]
func (c *ProjectController) UpdateProject(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectIDStr := ctx.Param("id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	var project projects_models.Project
	if err := ctx.ShouldBindJSON(&project); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	updatedProject, err := c.projectService.UpdateProject(projectID, &project, user)
	if err != nil {
		if err.Error() == "insufficient permissions to update project" {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, updatedProject)
}

// DeleteProject
// @Summary Delete project
// @Description Delete a project (owner only)
// @Tags projects
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id} [delete]
func (c *ProjectController) DeleteProject(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectIDStr := ctx.Param("id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	if err := c.projectService.DeleteProject(projectID, user); err != nil {
		if err.Error() == "only project owner or admin can delete project" {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Project deleted successfully"})
}

// GetProjectAuditLogs
// @Summary Get project audit logs
// @Description Retrieve audit logs for a specific project (member access required)
// @Tags projects
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Project ID"
// @Param limit query int false "Limit number of results" default(100)
// @Param offset query int false "Offset for pagination" default(0)
// @Param beforeDate query string false "Filter logs created before this date (RFC3339 format)" format(date-time)
// @Param user query string false "Search user name or email (case-insensitive substring)"
// @Param action query string false "Search the log message (case-insensitive substring)"
// @Param sortBy query string false "createdAt (default) or user" Enums(createdAt, user)
// @Param sortOrder query string false "desc (default) or asc" Enums(asc, desc)
// @Success 200 {object} audit_logs.GetAuditLogsResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /projects/{id}/audit-logs [get]
func (c *ProjectController) GetProjectAuditLogs(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	projectIDStr := ctx.Param("id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
		return
	}

	request := &audit_logs.GetAuditLogsRequest{}
	if err := ctx.ShouldBindQuery(request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid query parameters"})
		return
	}

	response, err := c.projectService.GetProjectAuditLogs(projectID, user, request)
	if err != nil {
		if err.Error() == "insufficient permissions to view project audit logs" {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}
