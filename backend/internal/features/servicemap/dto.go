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
}
