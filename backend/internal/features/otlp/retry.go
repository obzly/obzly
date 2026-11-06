package otlp

import (
	"net/http"
	"strconv"
	"time"

	"obzly/internal/ingest"

	"github.com/gin-gonic/gin"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Messages for the two JetStream ingest buffer failures.
const (
	IngestBufferFullMessage        = "ingest buffer is full, retry later"
	IngestBufferUnavailableMessage = "ingest buffer is unavailable, retry later"
)

// RetryableGRPCError is the status for a batch the ingest buffer did not
// take: RESOURCE_EXHAUSTED when full, UNAVAILABLE otherwise. OTLP exporters
// retry RESOURCE_EXHAUSTED only when RetryInfo is set.
func RetryableGRPCError(full bool, message string) error {
	if !full {
		return status.Error(codes.Unavailable, message)
	}
	st, err := status.New(codes.ResourceExhausted, message).WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(ingest.RetryAfterSeconds * time.Second),
	})
	if err != nil {
		return status.Error(codes.ResourceExhausted, message)
	}
	return st.Err()
}

// WriteRetryableHTTPStatus sets Retry-After and returns 429 when the buffer
// is full, 503 otherwise - the retryable OTLP/HTTP and remote-write
// statuses.
func WriteRetryableHTTPStatus(ctx *gin.Context, full bool) int {
	ctx.Header("Retry-After", strconv.Itoa(ingest.RetryAfterSeconds))
	if full {
		return http.StatusTooManyRequests
	}
	return http.StatusServiceUnavailable
}
