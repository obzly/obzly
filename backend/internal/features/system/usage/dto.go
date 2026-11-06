package system_usage

import "time"

// Response is a Site's data volume for Settings → Overview: what arrived
// per hour over the last day, and what is kept, by signal.
type Response struct {
	Hours   []Hour    `json:"hours"`
	Storage []Storage `json:"storage"`
}

type Hour struct {
	Hour time.Time `json:"hour"`
	// Signal is logs, metrics or traces.
	Signal   string `json:"signal"`
	RawBytes int64  `json:"rawBytes"`
	Rows     int64  `json:"rows"`
}

type Storage struct {
	Signal string `json:"signal"`
	// Bytes is compressed, as stored; RawBytes as received.
	Bytes    int64      `json:"bytes"`
	RawBytes int64      `json:"rawBytes"`
	Oldest   *time.Time `json:"oldest"`
}
