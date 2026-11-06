package logs_querying

import (
	"time"

	logs_core "obzly/internal/features/logs/core"
)

// Streamed search frame types (StreamQuery).
const (
	SearchFrameRows     = "rows"
	SearchFrameProgress = "progress"
	SearchFrameDone     = "done"
	SearchFrameError    = "error"
	// SearchFrameHeartbeat: the search is still running (every 10 s).
	SearchFrameHeartbeat = "heartbeat"
)

// SearchFrameDTO is one NDJSON line of a streamed search: "rows" (a
// window's rows past the offset, in page order), "progress" (a window read
// without adding any), then "done" or "error".
type SearchFrameDTO struct {
	Type         string                     `json:"type"`
	Logs         []logs_core.LogItemDTO     `json:"logs,omitempty"`
	Window       *logs_core.SearchWindowDTO `json:"window,omitempty"`
	NextCursor   string                     `json:"nextCursor,omitempty"`
	Total        *int64                     `json:"total,omitempty"`
	CountSkipped bool                       `json:"countSkipped,omitempty"`
	ExecutedInMs string                     `json:"executedIn,omitempty"`
	Status       int                        `json:"status,omitempty"`
	Error        string                     `json:"error,omitempty"`
	// Partial/SearchedThrough: see logs_core.LogQueryResponseDTO.
	Partial         bool       `json:"partial,omitempty"`
	SearchedThrough *time.Time `json:"searchedThrough,omitempty"`
}

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return e.Message
}
