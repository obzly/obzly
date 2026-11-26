package logs_core

import (
	"errors"
	"time"
)

// ErrSQLOutOfMemory: a SQL-mode query needed more memory than its DuckDB
// handle could get, spilling included.
var ErrSQLOutOfMemory = errors.New(
	"the query needed more memory than is available: narrow the time range or aggregate in fewer columns",
)

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	// RetryAfter: when a throttled batch may be sent again (RATE_LIMIT_EXCEEDED).
	RetryAfter time.Duration `json:"-"`
}

func (e *ValidationError) Error() string {
	return e.Message
}

const (
	ErrorProjectNotFound   = "PROJECT_NOT_FOUND"
	ErrorAPIKeyRequired    = "API_KEY_REQUIRED"
	ErrorAPIKeyInvalid     = "API_KEY_INVALID"
	ErrorDomainNotAllowed  = "DOMAIN_NOT_ALLOWED"
	ErrorIPNotAllowed      = "IP_NOT_ALLOWED"
	ErrorRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	ErrorLogTooLarge       = "LOG_TOO_LARGE"
	ErrorInvalidLogLevel   = "INVALID_LOG_LEVEL"

	ErrorProjectQuotaExceeded = "PROJECT_QUOTA_EXCEEDED"
	ErrorBatchTooLarge        = "BATCH_TOO_LARGE"
	ErrorMessageEmpty         = "MESSAGE_EMPTY"
	ErrorFutureTimestamp      = "FUTURE_TIMESTAMP"

	// JetStream ingest buffer (INGEST_BUFFER=jetstream): nothing in the
	// batch was accepted; the client retries the whole batch.
	ErrorIngestBufferFull  = "INGEST_BUFFER_FULL"
	ErrorIngestUnavailable = "INGEST_UNAVAILABLE"
)

// Error codes for log querying
const (
	ErrorTooManyConcurrentQueries = "TOO_MANY_CONCURRENT_QUERIES"
	ErrorInvalidQueryStructure    = "INVALID_QUERY_STRUCTURE"
	ErrorQueryTimeout             = "QUERY_TIMEOUT"
	ErrorQueryTooComplex          = "QUERY_TOO_COMPLEX"
	ErrorMissingTimeRangeTo       = "MISSING_TIME_RANGE_TO"
)
