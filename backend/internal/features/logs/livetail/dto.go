package logs_livetail

import logs_core "obzly/internal/features/logs/core"

// TailRequestDTO is the tail endpoint's body: only a filter, since time
// range, limit and sort mean nothing for a live stream. Nil Query tails all.
type TailRequestDTO struct {
	Query *logs_core.QueryNode `json:"query,omitempty"`
}

// tailFrame is one NDJSON line: "ready" (always first, used to detect proxy
// buffering), "heartbeat" (keepalive) or "log" (one matched log).
type tailFrame struct {
	Type string                `json:"type"`
	Log  *logs_core.LogItemDTO `json:"log,omitempty"`
}
