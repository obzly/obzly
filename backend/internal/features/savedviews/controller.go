package savedviews

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
)

const maxRequestBytes = 64 << 10

type Controller struct {
	service *Service
}

var controller = &Controller{&Service{func() time.Time { return time.Now().UTC() }}}

func GetController() *Controller { return controller }

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/saved-views", c.List)
	router.POST("/saved-views", c.Create)
	router.PUT("/saved-views/default", c.SetDefault)
	router.PUT("/saved-views/:id", c.Update)
	router.DELETE("/saved-views/:id", c.Delete)
}

// List
// @Summary Saved views
// @Description The user's own views of a kind, and those others shared, with the user's default
// @Tags saved-views
// @Produce json
// @Security BearerAuth
// @Param kind query string true "cost or posture"
// @Success 200 {object} savedviews.ViewList
// @Failure 400 {object} map[string]string
// @Router /saved-views [get]
func (c *Controller) List(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	list, err := c.service.List(user, ctx.Query("kind"))
	respond(ctx, list, err)
}

// Create
// @Summary Save a view
// @Description Private, or shared read-only with everyone
// @Tags saved-views
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body savedviews.ViewRequest true "View"
// @Success 200 {object} savedviews.ViewDTO
// @Failure 400 {object} map[string]string
// @Router /saved-views [post]
func (c *Controller) Create(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request ViewRequest
	if !bind(ctx, &request) {
		return
	}
	view, err := c.service.Create(user, request)
	respond(ctx, view, err)
}

// Update
// @Summary Change a saved view
// @Description Its owner only
// @Tags saved-views
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "View ID"
// @Param request body savedviews.ViewRequest true "View"
// @Success 200 {object} savedviews.ViewDTO
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /saved-views/{id} [put]
func (c *Controller) Update(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request ViewRequest
	if !bind(ctx, &request) {
		return
	}
	view, err := c.service.Update(user, id, request)
	respond(ctx, view, err)
}

// Delete
// @Summary Delete a saved view
// @Description Its owner only
// @Tags saved-views
// @Produce json
// @Security BearerAuth
// @Param id path string true "View ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /saved-views/{id} [delete]
func (c *Controller) Delete(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "View deleted"}, c.service.Delete(user, id))
}

// SetDefault
// @Summary Open a tab with a saved view
// @Description viewId null clears the default
// @Tags saved-views
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body savedviews.DefaultRequest true "Kind and view"
// @Success 200 {object} savedviews.ViewList
// @Failure 404 {object} map[string]string
// @Router /saved-views/default [put]
func (c *Controller) SetDefault(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request DefaultRequest
	if !bind(ctx, &request) {
		return
	}
	list, err := c.service.SetDefault(user, request)
	respond(ctx, list, err)
}

func currentUser(ctx *gin.Context) (*users_models.User, bool) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
	}
	return user, ok
}

func pathID(ctx *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return uuid.Nil, false
	}
	return id, true
}

func bind(ctx *gin.Context, target any) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxRequestBytes)
	if err := ctx.ShouldBindJSON(target); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
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
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
