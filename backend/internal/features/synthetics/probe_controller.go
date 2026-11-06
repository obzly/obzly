package synthetics

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"obzly/internal/synthetics/spec"
)

const probeContextKey = "synthetics_probe"

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ProbeController is the API obzly-probe calls, with its own token rather
// than a user session.
type ProbeController struct {
	service *Service
}

func (c *ProbeController) RegisterRoutes(router *gin.Engine) {
	group := router.Group("/api/v1/synthetics/probe", c.authenticate)
	group.GET("/config", c.GetConfig)
	group.GET("/tasks", c.NextTask)
	group.POST("/tasks/:id/result", c.PostResult)
	group.POST("/tasks/:id/cloud-result", c.PostCloudResult)
	group.POST("/cloud/:id/scan", c.ReportCloudScan)
	group.POST("/cloud/:id/token", c.CloudToken)
	group.POST("/tasks/:id/cloud-token", c.TaskCloudToken)
	group.POST("/leave", c.Leave)
}

// GetConfig
// @Summary A probe's monitors and destinations
// @Description Authenticated with the probe token. Answers 304 when If-None-Match matches
// @Tags synthetics-probe
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param If-None-Match header string false "ETag of the config the probe has"
// @Param X-Probe-Version header string false "obzly-probe version"
// @Success 200 {object} spec.ProbeConfig
// @Success 304
// @Failure 401 {object} map[string]string
// @Router /synthetics/probe/config [get]
func (c *ProbeController) GetConfig(ctx *gin.Context) {
	probe := probeFrom(ctx)
	config, etag, err := c.service.ProbeConfig(probe)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build the probe config"})
		return
	}
	ctx.Header("ETag", etag)
	c.members(ctx)
	if match := ctx.GetHeader("If-None-Match"); match != "" && match == etag {
		ctx.Status(http.StatusNotModified)
		return
	}
	ctx.JSON(http.StatusOK, config)
}

// NextTask
// @Summary Wait for an interactive test
// @Description Long-poll: returns a task as soon as one is queued for this probe, else 204 after wait seconds (at most 55)
// @Tags synthetics-probe
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param wait query int false "Seconds to wait (default 25, at most 55)"
// @Success 200 {object} spec.Task
// @Success 204
// @Failure 401 {object} map[string]string
// @Router /synthetics/probe/tasks [get]
func (c *ProbeController) NextTask(ctx *gin.Context) {
	wait := 25
	if raw := ctx.Query("wait"); raw != "" {
		parsed, err := strconv.Atoi(strings.TrimSuffix(raw, "s"))
		if err != nil || parsed < 1 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid wait"})
			return
		}
		wait = min(parsed, MaxTaskWaitSeconds)
	}
	task, err := c.service.NextTask(ctx.Request.Context(), probeFrom(ctx), time.Duration(wait)*time.Second)
	c.members(ctx)
	if errors.Is(err, ErrTooManyTaskPolls) {
		ctx.Header("Retry-After", "30")
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Tasks are temporarily unavailable"})
		return
	}
	if task == nil {
		ctx.Status(http.StatusNoContent)
		return
	}
	ctx.JSON(http.StatusOK, task)
}

// PostResult
// @Summary Report a test result
// @Tags synthetics-probe
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param id path string true "Task ID"
// @Param request body spec.RunResult true "Result"
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /synthetics/probe/tasks/{id}/result [post]
func (c *ProbeController) PostResult(ctx *gin.Context) {
	var result spec.RunResult
	if !bind(ctx, &result) {
		return
	}
	err := c.service.PostResult(ctx.Request.Context(), probeFrom(ctx), ctx.Param("id"), result)
	switch {
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "Unknown or expired task"})
	case err != nil:
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to store the result"})
	default:
		ctx.JSON(http.StatusOK, gin.H{"message": "Result stored"})
	}
}

// Leave
// @Summary An instance is stopping
// @Description Authenticated with the probe token. The instance named in X-Obzly-Probe-Instance leaves its group at once, so the other members take its monitors without waiting for it to go quiet
// @Tags synthetics-probe
// @Produce json
// @Param Authorization header string true "Bearer obz_prb_..."
// @Param X-Obzly-Probe-Instance header string false "The instance's name in its group"
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /synthetics/probe/leave [post]
func (c *ProbeController) Leave(ctx *gin.Context) {
	if err := c.service.LeaveGroup(probeFrom(ctx), instanceName(ctx.GetHeader(spec.HeaderInstance))); err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to record the instance leaving"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Left the group"})
}

func (c *ProbeController) authenticate(ctx *gin.Context) {
	token := strings.TrimPrefix(ctx.GetHeader("Authorization"), "Bearer ")
	probe, err := c.service.AuthenticateProbe(token)
	if errors.Is(err, ErrInvalidProbeToken) {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid probe token"})
		return
	}
	if err != nil {
		ctx.Header("Retry-After", "10")
		ctx.AbortWithStatusJSON(
			http.StatusServiceUnavailable,
			gin.H{"error": "Probe authentication is temporarily unavailable"},
		)
		return
	}
	checks, _ := strconv.Atoi(ctx.GetHeader(spec.HeaderChecks))
	if err := c.service.TouchProbe(
		probe,
		ctx.GetHeader("X-Probe-Version"),
		instanceName(ctx.GetHeader(spec.HeaderInstance)),
		checks,
	); errors.Is(err, ErrTooManyMembers) {
		ctx.Header("Retry-After", "60")
		ctx.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	ctx.Set(probeContextKey, probe)
	ctx.Next()
}

// instanceName is the name an instance reports, or "default" for probes
// from before groups (a group of one).
func instanceName(raw string) string {
	if !instancePattern.MatchString(raw) {
		return "default"
	}
	return raw
}

// members tells the instance who shares its group's monitors.
func (c *ProbeController) members(ctx *gin.Context) {
	ctx.Header(spec.HeaderMembers, strings.Join(c.service.LiveMembers(probeFrom(ctx)), ","))
}

func probeFrom(ctx *gin.Context) *Probe {
	probe, _ := ctx.MustGet(probeContextKey).(*Probe)
	return probe
}
