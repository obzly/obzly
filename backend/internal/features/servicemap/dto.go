package servicemap

// ServiceMapNodeDTO is one service. Sources ("spans", "network") lets the UI
// tell an instrumented service from one seen only on the network.
type ServiceMapNodeDTO struct {
	Name    string   `json:"name"`
	Sources []string `json:"sources"`

	// Omitted when no source reported them; never inferred.
	Namespace  string `json:"namespace,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Node       string `json:"node,omitempty"`

	// Type is service, database, cache, messaging or external; omitted when
	// unknown (a network-only node, or the "other" bucket).
	Type string `json:"type,omitempty"`
	// RED over the window: a service's entry spans, or the client spans
	// calling an untraced peer. Omitted for network-only nodes. P95LatencyMs
	// and ErrorRate are omitted with no requests (and P95 on "other").
	Requests     *int64   `json:"requests,omitempty"`
	Errors       *int64   `json:"errors,omitempty"`
	ErrorRate    *float64 `json:"errorRate,omitempty"`
	P95LatencyMs *float64 `json:"p95LatencyMs,omitempty"`
	// Health is ok, degraded or failing (see healthOf); omitted with no requests.
	Health string `json:"health,omitempty"`
}

// ServiceMapEdgeDTO is one src->dst pair. Stats are set only when a source
// that provides them contributed (spans: calls/errors/latency; network: bytes).
type ServiceMapEdgeDTO struct {
	Src     string   `json:"src"`
	Dst     string   `json:"dst"`
	Sources []string `json:"sources"`

	BytesTotal *int64   `json:"bytesTotal,omitempty"`
	CallCount  *int64   `json:"callCount,omitempty"`
	ErrorRate  *float64 `json:"errorRate,omitempty"`
	// AvgLatencyMs is weighted by call count across collapsed pairs.
	AvgLatencyMs *float64 `json:"avgLatencyMs,omitempty"`
}

type ServiceMapResponseDTO struct {
	Nodes []ServiceMapNodeDTO `json:"nodes"`
	Edges []ServiceMapEdgeDTO `json:"edges"`
	// WindowSeconds is the window after clamping, to turn counts into rates.
	WindowSeconds float64 `json:"windowSeconds"`
}
