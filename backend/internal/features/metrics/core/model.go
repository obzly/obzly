package metrics_core

import (
	"time"

	"github.com/google/uuid"
)

// MetricSample is one value at one instant for one series. As in
// Prometheus, each distinct MetricName + Labels set is its own series.
type MetricSample struct {
	ID         uuid.UUID         `json:"id"`
	ProjectID  uuid.UUID         `json:"projectId"`
	MetricName string            `json:"metricName"`
	Labels     map[string]string `json:"labels,omitempty"`
	Value      float64           `json:"value"`
	Timestamp  time.Time         `json:"timestamp"`
	Type       MetricType        `json:"type"`
}
