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

// RetryableGRPCError is the status for a batch the Site did not take:
// RESOURCE_EXHAUSTED when full or throttled, UNAVAILABLE otherwise. OTLP
// exporters retry RESOURCE_EXHAUSTED only when RetryInfo is set. retryAfter
// 0 means the ingest buffer's default.
func RetryableGRPCError(full bool, message string, retryAfter time.Duration) error {
	if !full {
		return status.Error(codes.Unavailable, message)
	}
	st, err := status.New(codes.ResourceExhausted, message).WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(retryDelay(retryAfter)),
	})
	if err != nil {
		return status.Error(codes.ResourceExhausted, message)
	}
	return st.Err()
}

// WriteRetryableHTTPStatus sets Retry-After and returns 429 when full or
// throttled, 503 otherwise - the retryable OTLP/HTTP and remote-write
// statuses. retryAfter 0 means the ingest buffer's default.
func WriteRetryableHTTPStatus(ctx *gin.Context, full bool, retryAfter time.Duration) int {
	ctx.Header("Retry-After", strconv.Itoa(RetryAfterSeconds(retryAfter)))
	if full {
		return http.StatusTooManyRequests
	}
	return http.StatusServiceUnavailable
}

// RetryAfterSeconds is a Retry-After header value: whole seconds, at least 1.
func RetryAfterSeconds(retryAfter time.Duration) int {
	return int((retryDelay(retryAfter) + time.Second - 1) / time.Second)
}

func retryDelay(retryAfter time.Duration) time.Duration {
	if retryAfter <= 0 {
		return ingest.RetryAfterSeconds * time.Second
	}
	return retryAfter
}
