package synthetics

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (c *Controller) registerMaintenanceRoutes(router *gin.RouterGroup) {
	router.GET("/synthetics/maintenance", c.ListMaintenance)
	router.POST("/synthetics/maintenance", c.CreateMaintenance)
	router.GET("/synthetics/maintenance/occurrences", c.MaintenanceOccurrences)
	router.POST("/synthetics/maintenance/preview", c.PreviewMaintenance)
	router.GET("/synthetics/maintenance/:id", c.GetMaintenance)
	router.PUT("/synthetics/maintenance/:id", c.UpdateMaintenance)
	router.DELETE("/synthetics/maintenance/:id", c.DeleteMaintenance)
	router.POST("/synthetics/maintenance/:id/end", c.EndMaintenance)
	router.POST("/synthetics/maintenance/:id/start", c.StartMaintenance)
}

// ListMaintenance
// @Summary List maintenance windows
// @Description Every window with its state (active, upcoming, past, paused), current or next occurrence and the monitors it covers
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Success 200 {array} synthetics.MaintenanceView
// @Failure 401 {object} map[string]string
// @Router /synthetics/maintenance [get]
func (c *Controller) ListMaintenance(ctx *gin.Context) {
	if _, ok := currentUser(ctx); !ok {
		return
	}
	windows, err := c.service.ListMaintenance()
	respond(ctx, windows, err)
}

// GetMaintenance
// @Summary Get a maintenance window
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Window ID"
// @Success 200 {object} synthetics.MaintenanceView
// @Failure 404 {object} map[string]string
// @Router /synthetics/maintenance/{id} [get]
func (c *Controller) GetMaintenance(ctx *gin.Context) {
	if _, ok := currentUser(ctx); !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	window, err := c.service.GetMaintenance(id)
	respond(ctx, window, err)
}

// CreateMaintenance
// @Summary Create a maintenance window
// @Description Admins only. A recurring schedule without a startDate starts today in its time zone
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.MaintenanceRequest true "Window"
// @Success 200 {object} synthetics.MaintenanceView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /synthetics/maintenance [post]
func (c *Controller) CreateMaintenance(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	var request MaintenanceRequest
	if !bind(ctx, &request) {
		return
	}
	window, err := c.service.CreateMaintenance(user, request)
	respond(ctx, window, err)
}

// UpdateMaintenance
// @Summary Update a maintenance window
// @Description Admins only. version must be the one loaded; 409 when someone saved in between
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Window ID"
// @Param request body synthetics.MaintenanceRequest true "Window"
// @Success 200 {object} synthetics.MaintenanceView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /synthetics/maintenance/{id} [put]
func (c *Controller) UpdateMaintenance(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	var request MaintenanceRequest
	if !bind(ctx, &request) {
		return
	}
	window, err := c.service.UpdateMaintenance(user, id, request)
	respond(ctx, window, err)
}

// DeleteMaintenance
// @Summary Delete a maintenance window
// @Description Admins only. Sites drop its silences at their next shared-config pull
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Window ID"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/maintenance/{id} [delete]
func (c *Controller) DeleteMaintenance(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Maintenance window deleted"}, c.service.DeleteMaintenance(user, id))
}

// EndMaintenance
// @Summary End a maintenance window's current occurrence now
// @Description Admins only; recorded in the audit log
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Window ID"
// @Success 200 {object} synthetics.MaintenanceView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/maintenance/{id}/end [post]
func (c *Controller) EndMaintenance(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	window, err := c.service.EndMaintenance(user, id)
	respond(ctx, window, err)
}

// StartMaintenance
// @Summary Start a maintenance window's next occurrence now
// @Description Admins only. It runs its planned duration from now; recorded in the audit log
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param id path string true "Window ID"
// @Success 200 {object} synthetics.MaintenanceView
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/maintenance/{id}/start [post]
func (c *Controller) StartMaintenance(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok {
		return
	}
	id, ok := pathID(ctx)
	if !ok {
		return
	}
	window, err := c.service.StartMaintenance(user, id)
	respond(ctx, window, err)
}

// MaintenanceOccurrences
// @Summary Maintenance occurrences in a time range
// @Description Occurrences of enabled windows overlapping [from, to) (RFC 3339, at most 93 days apart), each with the monitors it covers
// @Tags synthetics
// @Produce json
// @Security BearerAuth
// @Param from query string true "Start (RFC 3339)"
// @Param to query string true "End (RFC 3339)"
// @Success 200 {array} synthetics.MaintenanceOccurrence
// @Failure 400 {object} map[string]string
// @Router /synthetics/maintenance/occurrences [get]
func (c *Controller) MaintenanceOccurrences(ctx *gin.Context) {
	if _, ok := currentUser(ctx); !ok {
		return
	}
	from, errFrom := time.Parse(time.RFC3339, ctx.Query("from"))
	to, errTo := time.Parse(time.RFC3339, ctx.Query("to"))
	if errFrom != nil || errTo != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "from and to must be RFC 3339 times"})
		return
	}
	occurrences, err := c.service.MaintenanceOccurrences(from.UTC(), to.UTC())
	respond(ctx, occurrences, err)
}

// PreviewMaintenance
// @Summary Preview a maintenance schedule
// @Description Validates a schedule and returns its next occurrences (a one-off's only one), for the editor
// @Tags synthetics
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body synthetics.SchedulePreviewRequest true "Schedule"
// @Success 200 {object} synthetics.SchedulePreview
// @Failure 400 {object} map[string]string
// @Router /synthetics/maintenance/preview [post]
func (c *Controller) PreviewMaintenance(ctx *gin.Context) {
	if _, ok := currentUser(ctx); !ok {
		return
	}
	var request SchedulePreviewRequest
	if !bind(ctx, &request) {
		return
	}
	preview, err := c.service.PreviewSchedule(request)
	respond(ctx, preview, err)
}
