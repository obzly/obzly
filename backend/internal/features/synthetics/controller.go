package synthetics

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
)

// Request body limits: a monitor, a probe's result or scan, and an import
// (a YAML file of up to maxImportBytes, escaped in JSON).
const (
	maxRequestBytes = 1 << 20
	maxImportBody   = 3 << 20
)

type Controller struct {
	service *Service
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/synthetics/checks", c.ListChecks)
	router.POST("/synthetics/checks", c.CreateCheck)
	router.GET("/synthetics/checks/export", c.ExportChecks)
	router.POST("/synthetics/checks/import/preview", c.PreviewImport)
	router.POST("/synthetics/checks/import", c.ApplyImport)
	router.GET("/synthetics/checks/:id", c.GetCheck)
	router.PUT("/synthetics/checks/:id", c.UpdateCheck)
	router.DELETE("/synthetics/checks/:id", c.DeleteCheck)
	router.POST("/synthetics/checks/:id/enabled", c.SetCheckEnabled)
	router.POST("/synthetics/checks/:id/clone", c.CloneCheck)
	router.GET("/synthetics/destinations", c.ListDestinations)
	router.POST("/synthetics/destinations", c.CreateDestination)
	router.PUT("/synthetics/destinations/:id", c.UpdateDestination)
	router.DELETE("/synthetics/destinations/:id", c.DeleteDestination)
	router.GET("/synthetics/probes", c.ListProbes)
	router.POST("/synthetics/probes", c.CreateProbe)
	router.POST("/synthetics/probes/:id/token", c.RotateProbeToken)
	router.DELETE("/synthetics/probes/:id", c.DeleteProbe)
	router.POST("/synthetics/tests", c.CreateTest)
	router.GET("/synthetics/tests/:id", c.GetTest)
	router.GET("/cloud-connections", c.ListCloudConnections)
	router.POST("/cloud-connections", c.CreateCloudConnection)
	router.GET("/cloud-connections/setup", c.CloudSetup)
	router.POST("/cloud-connections/test", c.TestCloudConnection)
	router.GET("/cloud-connections/tests/:id", c.GetCloudTest)
	router.GET("/cloud-connections/:id", c.GetCloudConnection)
	router.PUT("/cloud-connections/:id", c.UpdateCloudConnection)
	router.DELETE("/cloud-connections/:id", c.DeleteCloudConnection)
	c.registerMaintenanceRoutes(router)
}

// ListChecks
// @Summary List synthetic monitors
// @Description Admins get whole definitions; other users a summary (no request headers, body, assertions or secret names)
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Success 200 {array} synthetics.CheckView
// @Failure 401 {object} map[string]string
// @Router /synthetics/checks [get]
func (c *Controller) ListChecks(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	checks, err := c.service.ListChecks(user)
	respond(ctx, checks, err)
}

// GetCheck
// @Summary Get a synthetic monitor
// @Description Admins get the whole definition; other users a summary
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Monitor ID"
// @Success 200 {object} synthetics.CheckView
// @Failure 404 {object} map[string]string
// @Router /synthetics/checks/{id} [get]
func (c *Controller) GetCheck(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	check, err := c.service.GetCheck(user, id)
	respond(ctx, check, err)
}

// CreateCheck
// @Summary Create a synthetic monitor
// @Description Admins only
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.CheckRequest true "Monitor"
// @Success 200 {object} synthetics.CheckView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /synthetics/checks [post]
func (c *Controller) CreateCheck(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request CheckRequest
	if !bind(ctx, &request) {
		return
	}
	check, err := c.service.CreateCheck(user, request)
	respond(ctx, check, err)
}

// UpdateCheck
// @Summary Update a synthetic monitor
// @Description Admins only. version must be the one loaded; 409 when someone saved in between
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Monitor ID"
// @Param request body synthetics.CheckRequest true "Monitor"
// @Success 200 {object} synthetics.CheckView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /synthetics/checks/{id} [put]
func (c *Controller) UpdateCheck(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request CheckRequest
	if !bind(ctx, &request) {
		return
	}
	check, err := c.service.UpdateCheck(user, id, request)
	respond(ctx, check, err)
}

// SetCheckEnabled
// @Summary Enable or disable a synthetic monitor
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Monitor ID"
// @Param request body synthetics.EnabledRequest true "State"
// @Success 200 {object} synthetics.CheckView
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /synthetics/checks/{id}/enabled [post]
func (c *Controller) SetCheckEnabled(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request EnabledRequest
	if !bind(ctx, &request) {
		return
	}
	check, err := c.service.SetCheckEnabled(user, id, request)
	respond(ctx, check, err)
}

// CloneCheck
// @Summary Clone a synthetic monitor
// @Description Creates a disabled "<name> (copy)" with the same definition and secrets
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Monitor ID"
// @Success 200 {object} synthetics.CheckView
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/checks/{id}/clone [post]
func (c *Controller) CloneCheck(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	check, err := c.service.CloneCheck(user, id)
	respond(ctx, check, err)
}

// DeleteCheck
// @Summary Delete a synthetic monitor
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Monitor ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/checks/{id} [delete]
func (c *Controller) DeleteCheck(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Monitor deleted"}, c.service.DeleteCheck(user, id))
}

// PreviewImport
// @Summary Preview a YAML import
// @Description What importing the file would create, update, leave and delete; nothing changes
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.ImportRequest true "File"
// @Success 200 {object} synthetics.ImportPreview
// @Failure 403 {object} map[string]string
// @Router /synthetics/checks/import/preview [post]
func (c *Controller) PreviewImport(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request ImportRequest
	if !bindLimited(ctx, &request, maxImportBody) {
		return
	}
	preview, err := c.service.PreviewImport(user, request)
	respond(ctx, preview, err)
}

// ApplyImport
// @Summary Import monitors from YAML
// @Description All or nothing: with any error in the file nothing is applied (applied=false)
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.ImportRequest true "File"
// @Success 200 {object} synthetics.ImportPreview
// @Failure 403 {object} map[string]string
// @Router /synthetics/checks/import [post]
func (c *Controller) ApplyImport(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request ImportRequest
	if !bindLimited(ctx, &request, maxImportBody) {
		return
	}
	preview, err := c.service.ApplyImport(user, request)
	respond(ctx, preview, err)
}

// ExportChecks
// @Summary Export monitors as YAML
// @Description Secrets are written as ${env:NAME} placeholders, never values
// @Tags synthetics
// @Produce plain
// @Security BearerAuth
// @Param ids query string false "Comma-separated monitor IDs (default all)"
// @Success 200 {string} string
// @Failure 403 {object} map[string]string
// @Router /synthetics/checks/export [get]
func (c *Controller) ExportChecks(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	if !user.CanUpdateSettings() {
		respond(ctx, nil, ErrForbidden)
		return
	}
	var ids []uuid.UUID
	for _, raw := range strings.Split(ctx.Query("ids"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ids"})
			return
		}
		ids = append(ids, id)
	}
	data, err := c.service.Export(ids)
	if err != nil {
		respond(ctx, nil, err)
		return
	}
	ctx.Header("Content-Disposition", `attachment; filename="synthetics-checks.yaml"`)
	ctx.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
}

// ListDestinations
// @Summary List synthetics destinations
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Success 200 {array} synthetics.Destination
// @Failure 403 {object} map[string]string
// @Router /synthetics/destinations [get]
func (c *Controller) ListDestinations(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	destinations, err := c.service.ListDestinations(user)
	respond(ctx, destinations, err)
}

// CreateDestination
// @Summary Create a synthetics destination
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.DestinationRequest true "Destination"
// @Success 200 {object} synthetics.Destination
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /synthetics/destinations [post]
func (c *Controller) CreateDestination(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request DestinationRequest
	if !bind(ctx, &request) {
		return
	}
	destination, err := c.service.CreateDestination(user, request)
	respond(ctx, destination, err)
}

// UpdateDestination
// @Summary Update a synthetics destination
// @Description An empty apiKey keeps the stored one
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Destination ID"
// @Param request body synthetics.DestinationRequest true "Destination"
// @Success 200 {object} synthetics.Destination
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/destinations/{id} [put]
func (c *Controller) UpdateDestination(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request DestinationRequest
	if !bind(ctx, &request) {
		return
	}
	destination, err := c.service.UpdateDestination(user, id, request)
	respond(ctx, destination, err)
}

// DeleteDestination
// @Summary Delete a synthetics destination
// @Description Refused (409) while monitors still use it
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Destination ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /synthetics/destinations/{id} [delete]
func (c *Controller) DeleteDestination(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Destination deleted"}, c.service.DeleteDestination(user, id))
}

// ListProbes
// @Summary List probes
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Success 200 {array} synthetics.ProbeView
// @Failure 403 {object} map[string]string
// @Router /synthetics/probes [get]
func (c *Controller) ListProbes(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	probes, err := c.service.ListProbes(user)
	respond(ctx, probes, err)
}

// CreateProbe
// @Summary Register a probe
// @Description Returns its token once
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.ProbeRequest true "Probe"
// @Success 200 {object} synthetics.ProbeTokenResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /synthetics/probes [post]
func (c *Controller) CreateProbe(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request ProbeRequest
	if !bind(ctx, &request) {
		return
	}
	response, err := c.service.CreateProbe(user, request)
	respond(ctx, response, err)
}

// RotateProbeToken
// @Summary Replace a probe's token
// @Description The old token keeps working for 24 hours so running instances can move over, or stops at once with revokeOld=true (a leaked token); the new one is returned once
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Probe ID"
// @Param revokeOld query bool false "Revoke the old tokens now"
// @Success 200 {object} synthetics.ProbeTokenResponse
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/probes/{id}/token [post]
func (c *Controller) RotateProbeToken(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	response, err := c.service.RotateProbeToken(user, id, ctx.Query("revokeOld") == "true")
	respond(ctx, response, err)
}

// DeleteProbe
// @Summary Delete a probe
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Probe ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/probes/{id} [delete]
func (c *Controller) DeleteProbe(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Probe deleted"}, c.service.DeleteProbe(user, id))
}

// CreateTest
// @Summary Test a monitor definition
// @Description Runs it once from each chosen location (or from the control plane); poll GET /synthetics/tests/{id}
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.TestRequest true "Test"
// @Success 200 {object} synthetics.TestRun
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /synthetics/tests [post]
func (c *Controller) CreateTest(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request TestRequest
	if !bind(ctx, &request) {
		return
	}
	run, err := c.service.CreateTest(ctx.Request.Context(), user, request)
	respond(ctx, run, err)
}

// GetTest
// @Summary Get a test's results so far
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Test ID"
// @Success 200 {object} synthetics.TestRun
// @Failure 404 {object} map[string]string
// @Router /synthetics/tests/{id} [get]
func (c *Controller) GetTest(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	run, err := c.service.GetTest(ctx.Request.Context(), user, ctx.Param("id"))
	respond(ctx, run, err)
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

// bind reads a JSON body of at most maxRequestBytes.
func bind(ctx *gin.Context, target any) bool {
	return bindLimited(ctx, target, maxRequestBytes)
}

func bindLimited(ctx *gin.Context, target any, limit int64) bool {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
	if err := ctx.ShouldBindJSON(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "The request is too large"})
			return false
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
}

// respond maps service errors: permission 403, missing 404, a stale or
// blocked change 409, anything else (validation) 400.
func respond(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, ErrConflict), errors.Is(err, ErrInUse), errors.Is(err, ErrCloudInUse),
		errors.Is(err, ErrMaintenanceConflict):
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
