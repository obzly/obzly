package metrics_core

import "time"

// TimeRangeDTO is duplicated per signal package rather than shared, so the
// signal packages stay independent.
type TimeRangeDTO struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

// MatcherDTO is one PromQL label matcher; Name "__name__" matches the metric
// name, as in PromQL.
type MatcherDTO struct {
	Name  string    `json:"name"`
	Type  MatchType `json:"type"`
	Value string    `json:"value"`
}

// SelectSeriesRequestDTO drives PromQL's storage.Querier.Select.
type SelectSeriesRequestDTO struct {
	Matchers  []MatcherDTO  `json:"matchers,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	// StepMs: the PromQL step; at 5 minutes or more, cold data is read from
	// rollups (first/min/max/last points per bucket) instead of every sample.
	StepMs int64 `json:"stepMs,omitempty"`
	// RangeMs: the selector's range window (the [5m] of rate(x[5m]); 0 for
	// none). A rollup is used only if the window spans at least
	// minBucketsPerRange of its buckets.
	RangeMs int64 `json:"rangeMs,omitempty"`
}

type SampleDTO struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// SeriesDTO is one time series. Samples are sorted by timestamp ascending
// (storage.NewListSeries requires that).
type SeriesDTO struct {
	MetricName string            `json:"metricName"`
	Labels     map[string]string `json:"labels,omitempty"`
	Samples    []SampleDTO       `json:"samples"`
}

type SelectSeriesResponseDTO struct {
	Series []SeriesDTO `json:"series"`
}

// RangeFunctionRequestDTO is one PromQL range function over one selector,
// optionally aggregated - e.g. sum by (job) (rate(x[5m])) - evaluated by
// storage at every step. Times are Unix ms, as the engine's; an instant
// query has StartMs = EndMs.
type RangeFunctionRequestDTO struct {
	Matchers    []MatcherDTO `json:"matchers"`
	Function    string       `json:"function"`
	RangeMs     int64        `json:"rangeMs"`
	StartMs     int64        `json:"startMs"`
	EndMs       int64        `json:"endMs"`
	StepMs      int64        `json:"stepMs"`
	Aggregation string       `json:"aggregation,omitempty"`
	By          []string     `json:"by,omitempty"`
}

// RangeFunctionResponseDTO is the result as series: the function's labels
// (the series' without the name, or the By labels when aggregated) with a
// point per step that has a value.
type RangeFunctionResponseDTO struct {
	Series []RangeSeriesDTO `json:"series"`
}

type RangeSeriesDTO struct {
	Labels map[string]string `json:"labels"`
	Points []PointDTO        `json:"points"`
}

type PointDTO struct {
	TimestampMs int64   `json:"t"`
	Value       float64 `json:"v"`
}

// MetricNameInfo is one metric name; Type is the most recently recorded type
// (a name that changes type is a misconfigured exporter).
type MetricNameInfo struct {
	Name string     `json:"name"`
	Type MetricType `json:"type"`
}

type ListMetricNamesResponseDTO struct {
	Metrics []MetricNameInfo `json:"metrics"`
}

type ListLabelNamesRequestDTO struct {
	Matchers  []MatcherDTO  `json:"matchers,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
}

type ListLabelNamesResponseDTO struct {
	Names []string `json:"names"`
}

// SeriesLifetimesRequestDTO selects the series of MetricNames last seen at
// or after ActiveSince; FirstSeenSince is how far back first seen looks.
type SeriesLifetimesRequestDTO struct {
	MetricNames    []string
	ActiveSince    time.Time
	FirstSeenSince time.Time
}

// SeriesLifetimeDTO is one series and when it was first and last seen.
type SeriesLifetimeDTO struct {
	MetricName string
	Labels     map[string]string
	FirstSeen  time.Time
	LastSeen   time.Time
}

type ListLabelValuesRequestDTO struct {
	LabelName string        `json:"labelName"`
	Matchers  []MatcherDTO  `json:"matchers,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
}

type ListLabelValuesResponseDTO struct {
	Values []string `json:"values"`
}

// MetricsStatsDTO is a project's metrics consumption stats.
type MetricsStatsDTO struct {
	TotalSamples int64 `json:"totalSamples"`
	// TotalSizeMB is the compressed on-disk Parquet size.
	TotalSizeMB float64 `json:"totalSizeMb"`
	// RawSizeMB is the approximate uncompressed size of the ingested samples.
	RawSizeMB        float64   `json:"rawSizeMb"`
	OldestSampleTime time.Time `json:"oldestSampleTime"`
	NewestSampleTime time.Time `json:"newestSampleTime"`
}
