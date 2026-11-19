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
// @Param history query bool false "Add the last 7 days' hourly counts by kind and those of 24 h ago"
// @Success 200 {object} inventory.SummaryDTO
// @Failure 403 {object} map[string]string
// @Router /inventory/summary [get]
func (c *Controller) GetSummary(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	summary, err := c.service.Summary(user, ctx.Query("projectId"), ctx.Query("history") == "true")
	respond(ctx, summary, err)
}

// ListAssets
// @Summary List inventory assets
// @Description A page of the matching assets, worst health first unless sort says otherwise; total is how many
// @Description matched. kind, health and state take comma-separated values. Without offset and limit it
// @Description returns the first 2000.
// @Tags inventory
// @Produce json
// @Security BearerAuth
// @Param projectId query string false "Only this project"
// @Param kind query string false "cluster, node, host, namespace, workload, pod, container, docker_container, service, load_balancer, database, bucket, function, scaling_group"
// @Param health query string false "healthy, warning, critical, inactive, unknown"
// @Param state query string false "new, active, not_reporting, terminated"
// @Param cluster query string false "Cluster name"
// @Param namespace query string false "Namespace"
// @Param parent query string false "Only the assets right under this asset key (use with projectId)"
// @Param roots query bool false "Only the assets without a parent in the inventory"
// @Param q query string false "Search name, node, attribute values and project name"
// @Param sort query string false "health (default), kind, name, cpu, memory, restarts, age, lastSeen or attention"
// @Param order query string false "asc (default: worst health, newest, least first) or desc (the reverse)"
// @Param offset query int false "Skip this many matches (default 0)"
// @Param limit query int false "At most this many (default 2000, max 5000)"
// @Param childCounts query bool false "Add each asset's childCount"
// @Param ancestors query bool false "Add the ancestors of the returned assets that are not among them"
// @Success 200 {object} inventory.AssetListDTO
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /inventory/assets [get]
func (c *Controller) ListAssets(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(ctx.Query("limit"))
	offset, _ := strconv.Atoi(ctx.Query("offset"))
	order := strings.TrimSpace(ctx.Query("order"))
	if order != "" && order != "asc" && order != "desc" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "order must be asc or desc"})
		return
	}
	filter := assetFilter{
		kinds:     list(ctx.Query("kind")),
		health:    list(ctx.Query("health")),
		states:    list(ctx.Query("state")),
		cluster:   ctx.Query("cluster"),
		namespace: ctx.Query("namespace"),
		search:    strings.TrimSpace(ctx.Query("q")),
		parent:    ctx.Query("parent"),
		roots:     ctx.Query("roots") == "true",
	}
	page := assetPage{
		sort:        strings.TrimSpace(ctx.Query("sort")),
		descending:  order == "desc",
		offset:      offset,
		limit:       limit,
		childCounts: ctx.Query("childCounts") == "true",
		ancestors:   ctx.Query("ancestors") == "true",
	}
	assets, err := c.service.ListAssets(user, ctx.Query("projectId"), filter, page)
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
	case errors.Is(err, ErrBadRequest):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
