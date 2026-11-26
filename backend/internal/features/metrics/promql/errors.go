package metrics_promql

import (
	"errors"

	metrics_core "obzly/internal/features/metrics/core"
)

// StorageError marks a failure of metrics storage under a query (a segment
// that can't be read, an executor gone): its text can name files, fragments
// and executors, so it is for the log, never for the caller. Everything else
// a query returns - a parse error, the engine's own limits and evaluation
// errors - comes from the expression and is safe to show.
type StorageError struct {
	Err error
}

func (e *StorageError) Error() string {
	return e.Err.Error()
}

func (e *StorageError) Unwrap() error {
	return e.Err
}

// ClientMessage is err's text for the caller of a query, false when err is
// a storage failure to log instead. A storage limit (metrics_core.LimitError)
// is the caller's to fix, so its own text is returned, without what wraps it.
func ClientMessage(err error) (string, bool) {
	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		return err.Error(), true
	}
	var limit *metrics_core.LimitError
	if errors.As(err, &limit) {
		return limit.Error(), true
	}
	return "", false
}
