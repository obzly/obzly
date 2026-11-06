package prometheus_remotewrite

import (
	"fmt"
	"io"
	"net/http"

	"obzly/internal/features/otlp"

	"github.com/gin-gonic/gin"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"

	"github.com/prometheus/prometheus/prompb"
)

// maxRemoteWriteBodyBytes caps the compressed body, which is read before the
// API key is checked. maxRemoteWriteDecodedBytes is checked before
// snappy.Decode, which allocates whatever length the body's header claims
// (up to ~4 GiB): a few bytes could otherwise exhaust memory.
const (
	maxRemoteWriteBodyBytes    = 32 << 20
	maxRemoteWriteDecodedBytes = 64 << 20
)

// remoteWriteVersionHeader is "2.0.0" for Remote Write 2.0 (unsupported).
const remoteWriteVersionHeader = "X-Prometheus-Remote-Write-Version"

// HTTPController serves POST /api/v1/write, Prometheus's own receiver path,
// so an existing remote_write block only needs its url and auth changed.
type HTTPController struct {
	service *RemoteWriteService
}

func (c *HTTPController) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/v1/write", c.Write)
}

func (c *HTTPController) Write(ctx *gin.Context) {
	if version := ctx.GetHeader(remoteWriteVersionHeader); len(version) > 0 && version[0] == '2' {
		ctx.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": "Remote Write 2.0 is not supported yet; configure this agent for the 1.0 protobuf protocol",
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxRemoteWriteBodyBytes))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	decodedLen, err := snappy.DecodedLen(body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to decompress snappy-encoded body"})
		return
	}
	if decodedLen > maxRemoteWriteDecodedBytes {
		ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("decompressed body exceeds %d byte limit", maxRemoteWriteDecodedBytes),
		})
		return
	}

	decompressed, err := snappy.Decode(nil, body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to decompress snappy-encoded body"})
		return
	}

	var request prompb.WriteRequest
	if err := proto.Unmarshal(decompressed, &request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid remote_write protobuf payload"})
		return
	}

	apiKey := apiKeyFromRequest(ctx)

	if _, _, err := c.service.Write(apiKey, &request); err != nil {
		c.writeError(ctx, err)
		return
	}

	ctx.Status(http.StatusNoContent)
}

// apiKeyFromRequest accepts X-API-Key or a Bearer token (what remote_write's
// `authorization` block sends). Basic auth is not supported.
func apiKeyFromRequest(ctx *gin.Context) string {
	if apiKey := ctx.GetHeader("X-API-Key"); apiKey != "" {
		return apiKey
	}
	if auth := ctx.GetHeader("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

func (c *HTTPController) writeError(ctx *gin.Context, err error) {
	writeErr, ok := err.(*WriteError)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to process remote-write request"})
		return
	}

	statusCode := http.StatusBadRequest
	switch writeErr.Code {
	case WriteErrorUnauthenticated:
		statusCode = http.StatusUnauthorized
	case WriteErrorResourceExhausted, WriteErrorUnavailable:
		// 503 even when full: Prometheus retries 429 only with
		// retry_on_http_429, but always retries 5xx.
		statusCode = otlp.WriteRetryableHTTPStatus(ctx, false)
	}
	ctx.JSON(statusCode, gin.H{"error": writeErr.Message})
}
