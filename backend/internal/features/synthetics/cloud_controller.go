package synthetics

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"obzly/internal/synthetics/spec"
)

// ListCloudConnections
// @Summary List cloud connections
// @Description Read-only access to AWS accounts and Azure subscriptions, scanned by probe groups. Identifiers only, never keys; the role or client ID, external ID and tenant ID for admins only
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Success 200 {array} synthetics.CloudConnectionView
// @Failure 401 {object} map[string]string
// @Router /cloud-connections [get]
func (c *Controller) ListCloudConnections(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	connections, err := c.service.ListCloudConnections(user)
	respond(ctx, connections, err)
}

// GetCloudConnection
// @Summary Get a cloud connection
// @Description The role or client ID, external ID and tenant ID for admins only
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} synthetics.CloudConnectionView
// @Failure 404 {object} map[string]string
// @Router /cloud-connections/{id} [get]
func (c *Controller) GetCloudConnection(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	connection, err := c.service.GetCloudConnection(user, id)
	respond(ctx, connection, err)
}

// CloudSetup
// @Summary Values for the connection wizard's templates
// @Description Admins only. A fresh external ID, the control plane's issuer URL and the federated token audiences
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Success 200 {object} synthetics.CloudSetup
// @Failure 403 {object} map[string]string
// @Router /cloud-connections/setup [get]
func (c *Controller) CloudSetup(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	setup, err := c.service.CloudSetup(user)
	respond(ctx, setup, err)
}

// CreateCloudConnection
// @Summary Create a cloud connection
// @Description Admins only
// @Tags cloud-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.CloudConnectionRequest true "Connection"
// @Success 200 {object} synthetics.CloudConnectionView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /cloud-connections [post]
func (c *Controller) CreateCloudConnection(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request CloudConnectionRequest
	if !bind(ctx, &request) {
		return
	}
	connection, err := c.service.CreateCloudConnection(user, request)
	respond(ctx, connection, err)
}

// UpdateCloudConnection
// @Summary Update a cloud connection
// @Description Admins only. version must be the one loaded; 409 when someone saved in between
// @Tags cloud-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Param request body synthetics.CloudConnectionRequest true "Connection"
// @Success 200 {object} synthetics.CloudConnectionView
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /cloud-connections/{id} [put]
func (c *Controller) UpdateCloudConnection(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request CloudConnectionRequest
	if !bind(ctx, &request) {
		return
	}
	connection, err := c.service.UpdateCloudConnection(user, id, request)
	respond(ctx, connection, err)
}

// DeleteCloudConnection
// @Summary Delete a cloud connection
// @Description Admins only. Its resources leave Infrastructure as they stop being seen
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /cloud-connections/{id} [delete]
func (c *Controller) DeleteCloudConnection(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Cloud connection deleted"}, c.service.DeleteCloudConnection(user, id))
}

// TestCloudConnection
// @Summary Test access of a connection, saved or not
// @Description Admins only. A live member of the probe group runs one dry scan per use; read the result from /cloud-connections/tests/{id}
// @Tags cloud-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.CloudConnectionRequest true "Connection"
// @Success 200 {object} synthetics.CloudTest
// @Failure 400 {object} map[string]string
// @Router /cloud-connections/test [post]
func (c *Controller) TestCloudConnection(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request CloudConnectionRequest
	if !bind(ctx, &request) {
		return
	}
	test, err := c.service.TestCloudConnection(ctx.Request.Context(), user, request)
	respond(ctx, test, err)
}

// GetCloudTest
// @Summary A Test access run and its result once it arrives
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Param id path string true "Test ID"
// @Success 200 {object} synthetics.CloudTest
// @Failure 404 {object} map[string]string
// @Router /cloud-connections/tests/{id} [get]
func (c *Controller) GetCloudTest(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	test, err := c.service.GetCloudTest(ctx.Request.Context(), user, ctx.Param("id"))
	respond(ctx, test, err)
}

// PostCloudResult
// @Summary A probe's Test access result
// @Tags synthetics-probe
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param id path string true "Task ID"
// @Param result body spec.CloudScan true "Scan"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/probe/tasks/{id}/cloud-result [post]
func (c *ProbeController) PostCloudResult(ctx *gin.Context) {
	var scan spec.CloudScan
	if !bind(ctx, &scan) {
		return
	}
	err := c.service.PostCloudTestResult(ctx.Request.Context(), probeFrom(ctx), ctx.Param("id"), scan)
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown or expired task"})
	case err != nil:
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to store the result"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"message": "Result stored"})
	}
}

// ReportCloudScan
// @Summary A probe's scheduled scan outcome
// @Tags synthetics-probe
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param id path string true "Connection ID"
// @Param scan body spec.CloudScan true "Scan"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/probe/cloud/{id}/scan [post]
func (c *ProbeController) ReportCloudScan(ctx *gin.Context) {
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var scan spec.CloudScan
	if !bind(ctx, &scan) {
		return
	}
	err := c.service.ReportCloudScan(probeFrom(ctx), id, scan)
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown cloud connection"})
	case err != nil:
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to store the scan"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"message": "Scan stored"})
	}
}

// TaskCloudToken
// @Summary A federated token for a Test access task
// @Description For an unsaved federated connection's Test access queued for this probe group (and not answered or expired yet); otherwise as /synthetics/probe/cloud/{id}/token
// @Tags synthetics-probe
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param id path string true "Task ID"
// @Success 200 {object} spec.CloudToken
// @Failure 404 {object} map[string]string
// @Router /synthetics/probe/tasks/{id}/cloud-token [post]
func (c *ProbeController) TaskCloudToken(ctx *gin.Context) {
	token, err := c.service.TaskCloudToken(ctx.Request.Context(), probeFrom(ctx), ctx.Param("id"))
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown, expired or non-federated test task"})
	case err != nil:
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to sign a token"})
	default:
		ctx.JSON(http.StatusOK, token)
	}
}

// CloudToken
// @Summary A federated connection's token for the cloud
// @Description Signed by the control plane (RS256, in its JWKS) for subject probe:<group ID>, the provider's audience, 10 minutes. The probe exchanges it for cloud credentials
// @Tags synthetics-probe
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param id path string true "Connection ID"
// @Success 200 {object} spec.CloudToken
// @Failure 404 {object} map[string]string
// @Router /synthetics/probe/cloud/{id}/token [post]
func (c *ProbeController) CloudToken(ctx *gin.Context) {
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	token, err := c.service.CloudToken(probeFrom(ctx), id)
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown or non-federated cloud connection"})
	case err != nil:
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to sign a token"})
	default:
		ctx.JSON(http.StatusOK, token)
	}
}

// GetCloudCostSettings
// @Summary Cost settings for every cloud connection
// @Description The tag keys kept on cost rows, to group and budget by
// @Tags cloud-connections
// @Produce json
// @Security BearerAuth
// @Success 200 {object} synthetics.CloudCostSettings
// @Failure 401 {object} map[string]string
// @Router /cloud-connections/cost-settings [get]
func (c *Controller) GetCloudCostSettings(ctx *gin.Context) {
	if _, ok := currentUser(ctx); !ok {
		return
	}
	settings, err := c.service.GetCloudCostSettings()
	respond(ctx, settings, err)
}

// UpdateCloudCostSettings
// @Summary Set the cost settings for every cloud connection
// @Description Admins only. Up to 10 tracked tag keys; probes get them with their next config
// @Tags cloud-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.CloudCostSettings true "Settings"
// @Success 200 {object} synthetics.CloudCostSettings
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /cloud-connections/cost-settings [put]
func (c *Controller) UpdateCloudCostSettings(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request CloudCostSettings
	if !bind(ctx, &request) {
		return
	}
	settings, err := c.service.UpdateCloudCostSettings(user, request)
	respond(ctx, settings, err)
}
