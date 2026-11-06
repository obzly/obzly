package logs_livetail

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// heartbeatInterval keeps the connection alive (and resets proxy idle
	// timeouts) between sparse matches; it is also the tail slot's refresh cadence.
	heartbeatInterval = 15 * time.Second
	// logFlushInterval coalesces bursts of matching logs into fewer Write+Flush
	// calls, short enough that viewers don't notice the delay.
	logFlushInterval = 150 * time.Millisecond
	// logFlushBatchCap flushes early so a bursty stream can't buffer unboundedly.
	logFlushBatchCap = 50
)

type Controller struct {
	service               *Service
	concurrentTailLimiter *ConcurrentTailLimiter
}

func NewController(service *Service, concurrentTailLimiter *ConcurrentTailLimiter) *Controller {
	return &Controller{service: service, concurrentTailLimiter: concurrentTailLimiter}
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.POST("/logs/tail/:projectId", c.TailLogs)
}

// TailLogs streams NDJSON tailFrames until the client disconnects. POST
// rather than EventSource GET so the filter is a JSON body and the normal
// Authorization header works.
//
// The first frame is always {"type":"ready"}: the client uses it to detect a
// buffering reverse proxy and fall back to polling. X-Accel-Buffering: no
// asks nginx-family proxies not to buffer (see docs/deployment, Live Tail).
//
// @Summary Stream matching logs in real time (Live Tail)
// @Description Newline-delimited JSON stream of logs matching the given query as they're ingested. Stays open until the client disconnects.
// @Tags logs-query
// @Accept json
// @Produce plain
// @Security BearerAuth
// @Param projectId path string true "Project ID (UUID format)"
// @Param request body TailRequestDTO true "Tail filter"
// @Success 200 {string} string "application/x-ndjson stream"
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /logs/tail/{projectId} [post]
func (c *Controller) TailLogs(ctx *gin.Context) {
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

	var request TailRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.service.CheckAccess(projectID, user); err != nil {
		if errors.Is(err, ErrForbidden) {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify project access"})
		}
		return
	}

	// Acquired after the access check but before any header is written, so a
	// rejection is a normal 429 JSON body, not a stream that goes silent.
	if err := c.concurrentTailLimiter.AcquireTailSlot(user.ID); err != nil {
		if errors.Is(err, ErrTooManyConcurrentTails) {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start live tail"})
		}
		return
	}
	defer c.concurrentTailLimiter.ReleaseTailSlot(user.ID)

	flusher, canFlush := ctx.Writer.(http.Flusher)
	if !canFlush {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming not supported"})
		return
	}

	ctx.Writer.Header().Set("Content-Type", "application/x-ndjson")
	ctx.Writer.Header().Set("Cache-Control", "no-cache")
	ctx.Writer.Header().Set("Connection", "keep-alive")
	ctx.Writer.Header().Set("X-Accel-Buffering", "no")
	ctx.Writer.WriteHeader(http.StatusOK)

	// Guards ctx.Writer (heartbeat, flush ticker and final drain all write;
	// gin's writer isn't concurrency-safe) and logBuffer (appended from Tail's
	// goroutine while the flush ticker drains it).
	var writeMu sync.Mutex
	var logBuffer []logs_core.LogItemDTO

	writeFrameLocked := func(frame tailFrame) error {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		_, err = ctx.Writer.Write(append(encoded, '\n'))
		return err
	}

	writeFrame := func(frame tailFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := writeFrameLocked(frame); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// flushLogBuffer writes everything buffered as "log" frames (still one per
	// log) followed by a single Flush.
	flushLogBuffer := func() error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if len(logBuffer) == 0 {
			return nil
		}
		pending := logBuffer
		logBuffer = nil
		for i := range pending {
			if err := writeFrameLocked(tailFrame{Type: "log", Log: &pending[i]}); err != nil {
				return err
			}
		}
		flusher.Flush()
		return nil
	}

	requestCtx := ctx.Request.Context()

	// The heartbeat starts only after "ready" is written, so a heartbeat can never
	// beat "ready" onto the wire (the client relies on ready being first).
	readyToHeartbeat := make(chan struct{})
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		select {
		case <-readyToHeartbeat:
		case <-requestCtx.Done():
			return
		case <-stopHeartbeat:
			return
		}

		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-requestCtx.Done():
				return
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				c.concurrentTailLimiter.RefreshTailSlot(user.ID)
				if writeFrame(tailFrame{Type: "heartbeat"}) != nil {
					return
				}
			}
		}
	}()

	// The flush ticker needs no readiness gate: appendLog only runs once the
	// subscription is live, which is after "ready" was written.
	stopFlush := make(chan struct{})
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		ticker := time.NewTicker(logFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-requestCtx.Done():
				return
			case <-stopFlush:
				return
			case <-ticker.C:
				if flushLogBuffer() != nil {
					return
				}
			}
		}
	}()

	onReady := func() {
		if writeFrame(tailFrame{Type: "ready"}) == nil {
			close(readyToHeartbeat)
		}
	}

	appendLog := func(dto logs_core.LogItemDTO) error {
		writeMu.Lock()
		logBuffer = append(logBuffer, dto)
		shouldFlush := len(logBuffer) >= logFlushBatchCap
		writeMu.Unlock()

		if shouldFlush {
			return flushLogBuffer()
		}
		return nil
	}

	_ = c.service.Tail(requestCtx, projectID, request.Query, user, onReady, appendLog)

	// Best-effort final drain; fails harmlessly if the client is gone.
	_ = flushLogBuffer()

	close(stopFlush)
	<-flushDone
	close(stopHeartbeat)
	<-heartbeatDone
}
