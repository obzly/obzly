package inventory

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/inventory/summary", c.GetSummary)
	router.GET("/inventory/assets", c.ListAssets)
	router.GET("/inventory/assets/:id", c.GetAsset)
	router.GET("/inventory/changes", c.ListChanges)
}

// GetSummary
// @Summary Count inventory assets by kind, health and state
// @Tags inventory
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Success 200 {object} inventory.SummaryDTO
// @Failure 403 {object} map[string]string
// @Router /inventory/summary [get]
func (c *Controller) GetSummary(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	summary, err := c.service.Summary(user, ctx.Query("projectId"))
	respond(ctx, summary, err)
}

// ListAssets
// @Summary List inventory assets
// @Description Worst health first. kind, health and state take comma-separated values.
// @Tags inventory
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param kind query string false "cluster, node, host, namespace, workload, pod, container, docker_container, service"
// @Param health query string false "healthy, warning, critical, inactive, unknown"
// @Param state query string false "new, active, not_reporting, terminated"
// @Param cluster query string false "Cluster name"
// @Param namespace query string false "Namespace"
// @Param q query string false "Search name, node and attributes"
// @Param limit query int false "At most this many (default 2000, max 5000)"
// @Success 200 {object} inventory.AssetListDTO
// @Failure 403 {object} map[string]string
// @Router /inventory/assets [get]
func (c *Controller) ListAssets(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(ctx.Query("limit"))
	filter := assetFilter{
		kinds:     list(ctx.Query("kind")),
		health:    list(ctx.Query("health")),
		states:    list(ctx.Query("state")),
		cluster:   ctx.Query("cluster"),
		namespace: ctx.Query("namespace"),
		search:    strings.TrimSpace(ctx.Query("q")),
	}
	assets, err := c.service.ListAssets(user, ctx.Query("projectId"), filter, limit)
	respond(ctx, assets, err)
}

// GetAsset
// @Summary Get an inventory asset with its parents, children and changes
// @Tags inventory
// @Produce json
// @Security BearerAuth
// @Param id path string true "Asset ID"
// @Success 200 {object} inventory.AssetDetailDTO
// @Failure 404 {object} map[string]string
// @Router /inventory/assets/{id} [get]
func (c *Controller) GetAsset(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}
	detail, err := c.service.GetAsset(user, id)
	respond(ctx, detail, err)
}

// ListChanges
// @Summary List recent inventory changes
// @Tags inventory
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param hours query int false "How far back (default 24, max 168)"
// @Param limit query int false "At most this many (default 100, max 500)"
// @Success 200 {array} inventory.ChangeDTO
// @Failure 403 {object} map[string]string
// @Router /inventory/changes [get]
func (c *Controller) ListChanges(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(ctx.Query("hours"))
	limit, _ := strconv.Atoi(ctx.Query("limit"))
	changes, err := c.service.ListChanges(user, ctx.Query("projectId"), hours, limit)
	respond(ctx, changes, err)
}

func currentUser(ctx *gin.Context) (*users_models.User, bool) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
	}
	return user, ok
}

func respond(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the inventory"})
	}
}

func list(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
