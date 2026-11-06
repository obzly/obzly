package otlp_traces

import (
	"fmt"
	"net/http"
	"strings"

	"obzly/internal/features/otlp"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// HTTPController is the OTLP/HTTP traces receiver: POST /v1/traces, protobuf or
// JSON, at the server root.
type HTTPController struct {
	service *OtlpTracesService
}

func (c *HTTPController) RegisterRoutes(router gin.IRouter) {
	router.POST("/v1/traces", c.Export)
}

func (c *HTTPController) Export(ctx *gin.Context) {
	body, err := otlp.ReadBody(ctx.Request)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	isJSON := strings.HasPrefix(ctx.GetHeader("Content-Type"), "application/json")

	var request collectortracev1.ExportTraceServiceRequest
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

	accepted, rejected, err := c.service.Export(apiKey, request.GetResourceSpans())
	if err != nil {
		c.writeError(ctx, err, isJSON)
		return
	}

	response := &collectortracev1.ExportTraceServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collectortracev1.ExportTracePartialSuccess{
			RejectedSpans: int64(rejected),
			ErrorMessage:  fmt.Sprintf("%d of %d spans rejected", rejected, accepted+rejected),
		}
	}

	c.writeResponse(ctx, http.StatusOK, response, isJSON)
}

func (c *HTTPController) writeError(ctx *gin.Context, err error, isJSON bool) {
	exportErr, ok := err.(*ExportError)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to process OTLP traces"})
		return
	}

	statusCode := http.StatusBadRequest
	switch exportErr.Code {
	case ExportErrorUnauthenticated:
		statusCode = http.StatusUnauthorized
	case ExportErrorResourceExhausted, ExportErrorUnavailable:
		statusCode = otlp.WriteRetryableHTTPStatus(ctx, exportErr.Code == ExportErrorResourceExhausted)
	}

	response := &collectortracev1.ExportTraceServiceResponse{
		PartialSuccess: &collectortracev1.ExportTracePartialSuccess{
			ErrorMessage: exportErr.Message,
		},
	}
	c.writeResponse(ctx, statusCode, response, isJSON)
}

func (c *HTTPController) writeResponse(
	ctx *gin.Context,
	statusCode int,
	response *collectortracev1.ExportTraceServiceResponse,
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
