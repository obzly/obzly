package otlp_logs

import (
	"fmt"
	"net/http"
	"strings"

	"obzly/internal/features/otlp"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// HTTPController is the OTLP/HTTP logs receiver: POST /v1/logs, protobuf or
// JSON. It's at the server root, the path every OTLP exporter uses.
type HTTPController struct {
	service *OtlpLogsService
}

func (c *HTTPController) RegisterRoutes(router gin.IRouter) {
	router.POST("/v1/logs", c.Export)
}

func (c *HTTPController) Export(ctx *gin.Context) {
	body, err := otlp.ReadBody(ctx.Request)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	isJSON := strings.HasPrefix(ctx.GetHeader("Content-Type"), "application/json")

	var request collectorlogsv1.ExportLogsServiceRequest
	if isJSON {
		if err := protojson.Unmarshal(body, &request); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid OTLP JSON payload"})
			return
		}
	} else {
		if err := proto.Unmarshal(body, &request); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid OTLP protobuf payload"})
			return
		}
	}

	apiKey := ctx.GetHeader("X-API-Key")
	clientIP := ctx.ClientIP()

	accepted, rejected, err := c.service.Export(apiKey, clientIP, request.GetResourceLogs())
	if err != nil {
		c.writeError(ctx, err, isJSON)
		return
	}

	response := &collectorlogsv1.ExportLogsServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collectorlogsv1.ExportLogsPartialSuccess{
			RejectedLogRecords: int64(rejected),
			ErrorMessage:       fmt.Sprintf("%d of %d log records rejected", rejected, accepted+rejected),
		}
	}

	c.writeResponse(ctx, http.StatusOK, response, isJSON)
}

func (c *HTTPController) writeError(ctx *gin.Context, err error, isJSON bool) {
	exportErr, ok := err.(*ExportError)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to process OTLP logs"})
		return
	}

	statusCode := http.StatusBadRequest
	switch exportErr.Code {
	case ExportErrorUnauthenticated:
		statusCode = http.StatusUnauthorized
	case ExportErrorResourceExhausted, ExportErrorUnavailable:
		statusCode = otlp.WriteRetryableHTTPStatus(ctx, exportErr.Code == ExportErrorResourceExhausted)
	}

	response := &collectorlogsv1.ExportLogsServiceResponse{
		PartialSuccess: &collectorlogsv1.ExportLogsPartialSuccess{
			ErrorMessage: exportErr.Message,
		},
	}
	c.writeResponse(ctx, statusCode, response, isJSON)
}

func (c *HTTPController) writeResponse(
	ctx *gin.Context,
	statusCode int,
	response *collectorlogsv1.ExportLogsServiceResponse,
	isJSON bool,
) {
	if isJSON {
		payload, err := protojson.Marshal(response)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode response"})
			return
		}
		ctx.Data(statusCode, "application/json", payload)
		return
	}

	payload, err := proto.Marshal(response)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode response"})
		return
	}
	ctx.Data(statusCode, "application/x-protobuf", payload)
}
