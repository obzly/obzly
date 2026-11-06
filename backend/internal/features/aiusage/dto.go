package aiusage

import "time"

type UsageFilter struct {
	From     time.Time
	To       time.Time
	User     string
	Model    string
	Terminal string
}

type OverviewRequest struct {
	UsageFilter
	BucketSeconds int64
	// GroupBy splits the timeseries: "model" (default) or "user".
	GroupBy string
}

type EventsRequest struct {
	UsageFilter
	// Event name without the "claude_code." prefix.
	Event     string
	SessionID string
	// Case-insensitive match anywhere in the event's fields.
	Search string
	Limit  int
	Offset int
}

type SessionsRequest struct {
	UsageFilter
	Search string
	// SortBy is lastSeen (default), cost, requests or tokens; always descending.
	SortBy string
	Limit  int
	Offset int
}

// CostUSD is Claude Code's API-equivalent estimate; on a subscription plan
// it isn't what was billed.
type UsageSummaryDTO struct {
	Requests            int64   `json:"requests"`
	CostUSD             float64 `json:"costUsd"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	Sessions            int64   `json:"sessions"`
	Users               int64   `json:"users"`
	Prompts             int64   `json:"prompts"`
	APIErrors           int64   `json:"apiErrors"`
	ToolCalls           int64   `json:"toolCalls"`
	AvgDurationMs       float64 `json:"avgDurationMs"`
	P50TtftMs           float64 `json:"p50TtftMs"`
}

type TimeseriesPointDTO struct {
	// Bucket start, Unix milliseconds.
	Bucket   int64   `json:"bucket"`
	Key      string  `json:"key"`
	CostUSD  float64 `json:"costUsd"`
	Tokens   int64   `json:"tokens"`
	Requests int64   `json:"requests"`
}

type BreakdownRowDTO struct {
	Key                 string  `json:"key"`
	Requests            int64   `json:"requests"`
	CostUSD             float64 `json:"costUsd"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	Sessions            int64   `json:"sessions"`
	LastSeen            int64   `json:"lastSeen"`
}

type ToolRowDTO struct {
	Tool          string  `json:"tool"`
	Calls         int64   `json:"calls"`
	Failures      int64   `json:"failures"`
	Rejections    int64   `json:"rejections"`
	AvgDurationMs float64 `json:"avgDurationMs"`
	P95DurationMs float64 `json:"p95DurationMs"`
}

type ErrorRowDTO struct {
	StatusCode string `json:"statusCode"`
	Error      string `json:"error"`
	Model      string `json:"model"`
	Count      int64  `json:"count"`
	LastSeen   int64  `json:"lastSeen"`
}

// FilterOptionsDTO ignores the active filters, so the pickers keep every value.
type FilterOptionsDTO struct {
	Users     []string `json:"users"`
	Models    []string `json:"models"`
	Terminals []string `json:"terminals"`
}

type OverviewResponseDTO struct {
	Summary UsageSummaryDTO `json:"summary"`
	// Newest matching event in the range, Unix ms (0 when there is none).
	LatestEventAt int64                `json:"latestEventAt"`
	Timeseries    []TimeseriesPointDTO `json:"timeseries"`
	BucketSeconds int64                `json:"bucketSeconds"`
	Users         []BreakdownRowDTO    `json:"users"`
	Models        []BreakdownRowDTO    `json:"models"`
	Tools         []ToolRowDTO         `json:"tools"`
	Errors        []ErrorRowDTO        `json:"errors"`
	FilterOptions FilterOptionsDTO     `json:"filterOptions"`
}

type SessionRowDTO struct {
	SessionID string   `json:"sessionId"`
	User      string   `json:"user"`
	Terminal  string   `json:"terminal"`
	FirstSeen int64    `json:"firstSeen"`
	LastSeen  int64    `json:"lastSeen"`
	Requests  int64    `json:"requests"`
	CostUSD   float64  `json:"costUsd"`
	Tokens    int64    `json:"tokens"`
	Prompts   int64    `json:"prompts"`
	ToolCalls int64    `json:"toolCalls"`
	Errors    int64    `json:"errors"`
	Models    []string `json:"models"`
}

type SessionsResponseDTO struct {
	Sessions []SessionRowDTO `json:"sessions"`
	Total    int64           `json:"total"`
}

type SessionEventDTO struct {
	// Unix milliseconds.
	Timestamp int64 `json:"timestamp"`
	// Event name without the "claude_code." prefix, e.g. "api_request".
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields"`
}

type SessionEventsResponseDTO struct {
	Events    []SessionEventDTO `json:"events"`
	Truncated bool              `json:"truncated"`
}

type EventsResponseDTO struct {
	// Newest first.
	Events []SessionEventDTO `json:"events"`
	Total  int64             `json:"total"`
	// Event names present in the range (ignoring the event, session and search filters).
	EventTypes []string `json:"eventTypes"`
}
