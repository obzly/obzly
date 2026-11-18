package logs_core

import (
	"strings"
	"time"
)

// Several group fields in AggregationRequestDTO.GroupField are joined by
// GroupFieldsSeparator; each group key then holds their values joined by
// GroupKeySeparator (a value never contains it: it is a control character).
const (
	GroupFieldsSeparator = ","
	GroupKeySeparator    = "\x1f"
)

// Health map groupings (HealthMapRequestDTO.GroupBy).
const (
	HealthMapByKubernetes = "k8s"
	HealthMapByHost       = "host"
	HealthMapByService    = "service"
)

// PatternSamplesRequestDTO asks for the newest log of each of Hashes among
// the logs matching Query in TimeRange.
type PatternSamplesRequestDTO struct {
	Query     *QueryNode
	TimeRange *TimeRangeDTO
	Hashes    []string
}

// PatternSampleDTO is a pattern's template, newest log and its level.
type PatternSampleDTO struct {
	Pattern string `json:"pattern"`
	Sample  string `json:"sample"`
	Level   string `json:"level"`
}

// PatternTrendsRequestDTO asks for the Logs Patterns tab: message patterns'
// counts over TimeRange (From and To required) in BucketMs buckets.
type PatternTrendsRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange"`
	// BucketMs: whole minutes; 0 picks one giving at most 60 buckets.
	BucketMs int64 `json:"bucketMs,omitempty"`
	// Limit: the biggest patterns listed (changed ones are listed too); 0 means 30, at most 50.
	Limit int `json:"limit,omitempty"`
}

// PatternTrendDTO is one pattern on the Patterns tab. Change is "new" (not
// seen in the 7 days before the range nor 24 h earlier), "spike" (at least
// twice its count 24 h earlier), "drop" (at most half of it) or "".
type PatternTrendDTO struct {
	Hash    string `json:"hash"`
	Pattern string `json:"pattern"`
	Sample  string `json:"sample"`
	// Level: the level most of its lines have.
	Level string `json:"level"`
	// Services: the services logging it, most lines first (at most 5).
	Services []string `json:"services"`
	Count    int64    `json:"count"`
	// Series: lines per bucket, one per PatternTrendsResponseDTO.Buckets.
	Series []int64 `json:"series"`
	// Yesterday: lines in the same window 24 h earlier.
	Yesterday int64 `json:"yesterday"`
	// PreviousWeek: lines in the 7 days before the range.
	PreviousWeek int64  `json:"previousWeek"`
	Change       string `json:"change"`
}

type PatternTrendsResponseDTO struct {
	BucketMs int64       `json:"bucketMs"`
	Buckets  []time.Time `json:"buckets"`
	// Total: lines in the range; Distinct: patterns among them.
	Total    int64 `json:"total"`
	Distinct int   `json:"distinct"`
	// Other: lines per bucket of the patterns not listed.
	Other      []int64           `json:"other"`
	NewCount   int               `json:"newCount"`
	SpikeCount int               `json:"spikeCount"`
	DropCount  int               `json:"dropCount"`
	Patterns   []PatternTrendDTO `json:"patterns"`
	// Partial: more patterns than one query counts; the smallest are left out.
	Partial bool `json:"partial"`
}

// ExplainRequestDTO asks what is over-represented in Current against
// Baseline (both with From and To) among the logs matching Query.
type ExplainRequestDTO struct {
	Query    *QueryNode    `json:"query,omitempty"`
	Current  *TimeRangeDTO `json:"current"`
	Baseline *TimeRangeDTO `json:"baseline"`
	// MinCount: fewest lines in Current for a value to be listed; 0 means 20.
	MinCount int64 `json:"minCount,omitempty"`
}

// ExplainFieldDTO is one field value's lines and share of all lines in each
// range. Field is a filterable field, except k8s_replicaset (a pod name
// without its last part), which narrows on k8s_pod_name containing it.
type ExplainFieldDTO struct {
	Field         string  `json:"field"`
	Value         string  `json:"value"`
	Current       int64   `json:"current"`
	Baseline      int64   `json:"baseline"`
	CurrentShare  float64 `json:"currentShare"`
	BaselineShare float64 `json:"baselineShare"`
}

type ExplainResponseDTO struct {
	CurrentTotal  int64 `json:"currentTotal"`
	BaselineTotal int64 `json:"baselineTotal"`
	// Fields: values whose share grew most, largest gain first.
	Fields  []ExplainFieldDTO `json:"fields"`
	Partial bool              `json:"partial"`
}

// VolumeRequestDTO asks for the Volume & noise tab over the last Days UTC
// days (today included).
type VolumeRequestDTO struct {
	// Days: 0 means 14, at most 31.
	Days int `json:"days,omitempty"`
}

// VolumeSeriesDTO is one service's lines per day; Other marks the rest of
// the services together.
type VolumeSeriesDTO struct {
	Name   string  `json:"name"`
	Counts []int64 `json:"counts"`
	Total  int64   `json:"total"`
	Other  bool    `json:"other,omitempty"`
}

type VolumeCountDTO struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// NoisyPatternDTO is a pattern of one service at one level, with its lines
// over the last 7 days.
type NoisyPatternDTO struct {
	Hash    string `json:"hash"`
	Pattern string `json:"pattern"`
	Sample  string `json:"sample"`
	Service string `json:"service"`
	Level   string `json:"level"`
	Count   int64  `json:"count"`
	// PerDay: Count over the days of the 7 that had logs.
	PerDay float64 `json:"perDay"`
}

type VolumeResponseDTO struct {
	Days     []time.Time       `json:"days"`
	Services []VolumeSeriesDTO `json:"services"`
	// Levels: lines per level over the last 7 days.
	Levels []VolumeCountDTO `json:"levels"`
	// Today: lines since 00:00 UTC; YesterdaySoFar: lines yesterday up to the same time.
	Today          int64 `json:"today"`
	YesterdaySoFar int64 `json:"yesterdaySoFar"`
	// BytesPerLine / RawBytesPerLine: the project's stored (compressed) and
	// ingested bytes per line; 0 when it has no logs.
	BytesPerLine    float64           `json:"bytesPerLine"`
	RawBytesPerLine float64           `json:"rawBytesPerLine"`
	StoredBytes     int64             `json:"storedBytes"`
	Noisy           []NoisyPatternDTO `json:"noisy"`
	Partial         bool              `json:"partial"`
}

// HealthMapRequestDTO asks for the Health map tab over TimeRange (From and
// To required): log volume and error share per group and tile.
type HealthMapRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange"`
	// GroupBy: k8s (namespace and workload), host (host and service) or service.
	GroupBy string `json:"groupBy"`
}

// HealthTileDTO is one workload (or service): its lines, error and warning
// lines, and lines in the same window 24 h earlier.
type HealthTileDTO struct {
	Name     string `json:"name"`
	Total    int64  `json:"total"`
	Errors   int64  `json:"errors"`
	Warnings int64  `json:"warnings"`
	Previous int64  `json:"previous"`
}

type HealthGroupDTO struct {
	Name  string          `json:"name"`
	Total int64           `json:"total"`
	Tiles []HealthTileDTO `json:"tiles"`
}

type HealthMapResponseDTO struct {
	GroupBy string           `json:"groupBy"`
	Groups  []HealthGroupDTO `json:"groups"`
	// HiddenTiles: smaller tiles left out to keep the map readable.
	HiddenTiles int  `json:"hiddenTiles"`
	Partial     bool `json:"partial"`
}

// JoinGroupFields is the GroupField counting by every one of fields.
func JoinGroupFields(fields ...string) string {
	return strings.Join(fields, GroupFieldsSeparator)
}

// SplitGroupKey is a composite group key's n values ("" where missing).
func SplitGroupKey(key string, n int) []string {
	values := strings.SplitN(key, GroupKeySeparator, n)
	for len(values) < n {
		values = append(values, "")
	}
	return values
}
