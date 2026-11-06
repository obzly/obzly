package logs_core

import "time"

// LogQueryRequestDTO is a logs search request.
type LogQueryRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	Limit     int           `json:"limit,omitempty"`
	Offset    int           `json:"offset,omitempty"`
	SortBy    string        `json:"sortBy,omitempty"`    // always "timestamp" for now
	SortOrder string        `json:"sortOrder,omitempty"` // "asc" or "desc"
	// TrackTotal false skips counting matches (Total is then -1); absent
	// counts, as before it was honoured.
	TrackTotal *bool `json:"trackTotal,omitempty"`
	// Cursor continues from a previous response's NextCursor; Offset is
	// ignored with it, and Total is -1 (it was counted on the first page).
	Cursor string `json:"cursor,omitempty"`
	// ScanBudget is set from QUERY_MAX_SCAN_GB, never from JSON; 0 for none.
	ScanBudget int64 `json:"-"`
	// TimestampsOnly: rows carry only id and timestamp. The quota cutoff's deep
	// offset holds offset+limit rows, which exceeded worker memory with every column.
	TimestampsOnly bool `json:"-"`
}

// TotalNotCounted is LogQueryResponseDTO.Total when no count was made.
const TotalNotCounted = -1

type TimeRangeDTO struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

// ExecuteSQLQueryRequestDTO is a user-written SELECT for the logs page's SQL
// mode, run against a "logs" table scoped to the project and TimeRange (see
// LogQuerier.ExecuteSQLQuery for the security model).
type ExecuteSQLQueryRequestDTO struct {
	SQL       string        `json:"sql"                 binding:"required"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB.
	ScanBudget int64 `json:"-"`
}

// SQLQueryResponseDTO is generic (columns + rows) because user SQL can
// project any columns.
type SQLQueryResponseDTO struct {
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowCount     int      `json:"rowCount"`
	Truncated    bool     `json:"truncated"`
	ExecutedInMs string   `json:"executedInMs"`
}

// ScopedSQLRequestDTO is for trusted, server-built statements only (never
// user SQL): they share one "logs" table materialized for the time range and
// message prefix. See LogQuerier.ExecuteScopedSQL.
type ScopedSQLRequestDTO struct {
	From          time.Time
	To            time.Time
	MessagePrefix string
	Statements    []ScopedSQLStatement
	// ScanBudget refuses a range whose segments add up to more (0: none).
	ScanBudget int64
}

type ScopedSQLStatement struct {
	SQL  string
	Args []any
}

type LogQueryResponseDTO struct {
	Logs         []LogItemDTO `json:"logs"`
	Total        int64        `json:"total"`
	Limit        int          `json:"limit"`
	Offset       int          `json:"offset"`
	ExecutedInMs string       `json:"executedIn"`
	// NextCursor fetches the page after this one; empty on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
	// CountSkipped: counting would read more than the scan budget, so
	// Total is -1; the page itself is complete.
	CountSkipped bool `json:"countSkipped,omitempty"`
	// Partial: the search reached its time limit before the page was full
	// or the range was read; it covered the range from its newest end back
	// to SearchedThrough (from the oldest end up to it, ascending).
	Partial         bool       `json:"partial,omitempty"`
	SearchedThrough *time.Time `json:"searchedThrough,omitempty"`
}

// SearchWindowDTO is the time window a streamed search frame covers.
type SearchWindowDTO struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// SearchEmitter receives a streamed search's rows window by window, in page
// order: the rows each window adds past the requested offset, possibly none
// for a window that only advanced the search.
type SearchEmitter func(logs []LogItemDTO, window SearchWindowDTO) error

type LogItemDTO struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	ClientIP  string         `json:"clientIp,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	TraceID   string         `json:"traceId,omitempty"`
	SpanID    string         `json:"spanId,omitempty"`
	// PatternHash is this log's pattern.hash (see pattern.go) - lets the UI
	// offer "show only / hide logs like this" on a row without a round
	// trip. Computed at read time, per returned row.
	PatternHash string `json:"patternHash,omitempty"`
}

// QueryNode is a node of the storage-agnostic query AST.
type QueryNode struct {
	Type      QueryNodeType  `json:"type"`
	Logic     *LogicalNode   `json:"logic,omitempty"`
	Condition *ConditionNode `json:"condition,omitempty"`
}

type LogicalNode struct {
	Operator LogicalOperator `json:"operator"`
	Children []QueryNode     `json:"children"`
}

type ConditionNode struct {
	Field    string            `json:"field"`
	Operator ConditionOperator `json:"operator"`
	Value    any               `json:"value"`
}

type QueryableField struct {
	Name       string              `json:"name"`
	Type       QueryableFieldType  `json:"type"`
	Operations []ConditionOperator `json:"operations"`
	IsCustom   bool                `json:"isCustom"` // non-system field
}

type GetQueryableFieldsRequestDTO struct {
	Query string `form:"query" json:"query"`
}

type GetQueryableFieldsResponseDTO struct {
	Fields []QueryableField `json:"fields"`
}

// AggregationRequestDTO drives dashboard panel data. BucketMs and GroupField
// are independently optional: time series, grouped time series, top-N
// breakdown, or a single total.
type AggregationRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	// GroupField: a system or custom field, resolved as a query condition's
	// field is. "" means no grouping.
	GroupField string `json:"groupField,omitempty"`
	// BucketMs: time-bucket width in ms; 0 aggregates the whole range.
	BucketMs int64 `json:"bucketMs,omitempty"`
	// Limit: max buckets/groups (the N of a top-N, else a safety cap). 0 means
	// the querier's default.
	Limit int `json:"limit,omitempty"`
	// ScanBudget is set from QUERY_MAX_SCAN_GB, never from JSON; 0 for none.
	ScanBudget int64 `json:"-"`
}

// AggregationEmitter receives a streamed aggregation's counts as they are
// read: each call's buckets add to the ones before (the same bucket and
// group can come again, from another partition).
type AggregationEmitter func(buckets []AggregationBucketDTO) error

type AggregationBucketDTO struct {
	// Timestamp is nil when the request had no BucketMs.
	Timestamp *time.Time `json:"timestamp,omitempty"`
	// GroupKey is "" when the request had no GroupField (a single series).
	GroupKey string `json:"groupKey,omitempty"`
	Count    int64  `json:"count"`
}

type AggregationResponseDTO struct {
	Buckets []AggregationBucketDTO `json:"buckets"`
}

// PatternsRequestDTO asks for the most frequent message patterns (see
// PatternRules) among the logs matching Query in TimeRange - the logs
// page's "pattern.hash" facet.
type PatternsRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	// Limit: max patterns returned, most frequent first. 0 means the
	// querier's default.
	Limit int `json:"limit,omitempty"`
	// SampleRows > 0 reads the range from its newest end and stops once
	// at least this many matching logs were counted (see SampledFrom); 0
	// reads the whole range.
	SampleRows int64 `json:"sampleRows,omitempty"`
	// ScanBudget is set from QUERY_MAX_SCAN_GB, never from JSON; 0 for none.
	ScanBudget int64 `json:"-"`
}

// LogPatternDTO is one message template: Hash is its PatternHashField
// value (what a filter on it matches), Pattern the masked template, Sample
// one real message it came from.
type LogPatternDTO struct {
	Hash    string `json:"hash"`
	Pattern string `json:"pattern"`
	Sample  string `json:"sample"`
	Count   int64  `json:"count"`
}

type PatternsResponseDTO struct {
	Patterns []LogPatternDTO `json:"patterns"`
	FacetSampleDTO
}

// FacetSampleDTO says what a facet asked with SampleRows counted.
type FacetSampleDTO struct {
	// SampledFrom is set when the facet stopped early: it counted the
	// matching logs from here to the range's end, not the whole range.
	SampledFrom *time.Time `json:"sampledFrom,omitempty"`
	// MatchedRows is how many matching logs it counted.
	MatchedRows int64 `json:"matchedRows"`
}

// FieldValuesRequestDTO asks for a field's most frequent values among the
// logs matching Query in TimeRange - the logs page's field facets and
// search bar suggestions.
type FieldValuesRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
	// Field: a system field (e.g. "level") or a custom one, as in a query
	// condition.
	Field string `json:"field" binding:"required"`
	// Limit: max values returned, most frequent first. 0 means the
	// querier's default.
	Limit int `json:"limit,omitempty"`
	// SampleRows: as PatternsRequestDTO.SampleRows.
	SampleRows int64 `json:"sampleRows,omitempty"`
	// IncludeRecent: for a log source field (SourceIndexFields), also list
	// the values its sources had in the last RecentValuesWindow or in the
	// range that Values lacks (see FieldValuesResponseDTO.Recent).
	IncludeRecent bool `json:"includeRecent,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB.
	ScanBudget int64 `json:"-"`
}

type RecentFieldValueDTO struct {
	Value    string    `json:"value"`
	LastSeen time.Time `json:"lastSeen"`
	// InRange: LastSeen is inside the time range, so the value has logs
	// there that Values left out (the sample or the limit).
	InRange bool `json:"inRange,omitempty"`
}

type FieldValueDTO struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type FieldValuesResponseDTO struct {
	Values []FieldValueDTO `json:"values"`
	// Recent: with IncludeRecent, values not in Values whose sources logged
	// in the last RecentValuesWindow or in the range, newest first. Read
	// from log_source_index, so without counts.
	Recent []RecentFieldValueDTO `json:"recent,omitempty"`
	// RecentSkipped: Recent was not computed because the query filters on
	// something other than log source fields and level.
	RecentSkipped bool `json:"recentSkipped,omitempty"`
	FacetSampleDTO
}

type LogsStatsDTO struct {
	TotalLogs int64 `json:"totalLogs"`
	// TotalSizeMB is the compressed on-disk Parquet size.
	TotalSizeMB float64 `json:"totalSizeMb"`
	// RawSizeMB is the approximate uncompressed size of the ingested rows
	// (before Parquet/columnar compression) - "log ingested size".
	RawSizeMB     float64   `json:"rawSizeMb"`
	OldestLogTime time.Time `json:"oldestLogTime"`
	NewestLogTime time.Time `json:"newestLogTime"`
}

// SystemDashboardDTO is the admin System Dashboard's payload: LogsStatsDTO's
// fields (kept flat) plus host resource usage and recent ingestion rate.
type SystemDashboardDTO struct {
	TotalLogs     int64     `json:"totalLogs"`
	TotalSizeMB   float64   `json:"totalSizeMb"`
	RawSizeMB     float64   `json:"rawSizeMb"`
	OldestLogTime time.Time `json:"oldestLogTime"`
	NewestLogTime time.Time `json:"newestLogTime"`

	// Ingestion, last 15 minutes.
	IngestionWindowMinutes int     `json:"ingestionWindowMinutes"`
	IngestedCount          int64   `json:"ingestedCount"`
	IngestedRawBytes       int64   `json:"ingestedRawBytes"`
	AvgIngestionRatePerMin float64 `json:"avgIngestionRatePerMin"`

	// Host resource usage. See system_metrics.HostMetrics for the
	// container/host-visibility caveat.
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  int64   `json:"memoryUsedBytes"`
	MemoryTotalBytes int64   `json:"memoryTotalBytes"`
	DiskUsedBytes    int64   `json:"diskUsedBytes"`
	DiskTotalBytes   int64   `json:"diskTotalBytes"`
}

var PredefinedQueryableFields = []QueryableField{
	{
		Name: "message",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorContains, ConditionOperatorNotContains,
		},
	},
	{
		Name: "level",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
		},
	},
	{
		Name: "client_ip",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorContains, ConditionOperatorNotContains,
		},
	},
	{
		// Virtual, computed from message (see pattern.go).
		Name: PatternHashField,
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
		},
	},
	{
		Name: "timestamp",
		Type: QueryableFieldTypeTimestamp,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorGreaterThan, ConditionOperatorGreaterOrEqual,
			ConditionOperatorLessThan, ConditionOperatorLessOrEqual,
		},
	},
	{
		// Hex-encoded W3C trace/span IDs.
		Name: "trace_id",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "span_id",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	// Promoted OTel resource attributes: real columns, not fields JSON lookups
	// (see PromotedResourceColumns).
	{
		Name: "service_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "service_version",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "k8s_namespace_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "k8s_pod_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "k8s_container_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "k8s_node_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "k8s_cluster_name",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "cloud_provider",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name: "cloud_region",
		Type: QueryableFieldTypeString,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorIn, ConditionOperatorNotIn,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
	{
		Name:     "fields.*",
		Type:     QueryableFieldTypeString, // Default, can be overridden
		IsCustom: true,
		Operations: []ConditionOperator{
			ConditionOperatorEquals, ConditionOperatorNotEquals,
			ConditionOperatorContains, ConditionOperatorNotContains,
			ConditionOperatorExists, ConditionOperatorNotExists,
		},
	},
}

// LogContextRequestDTO asks for the logs around one log (the anchor): the
// ones just before and after it from the same source, whatever the search
// that found it - what led up to an error is usually filtered out of it.
type LogContextRequestDTO struct {
	Anchor LogContextAnchorDTO `json:"anchor"`
	// Before / After: how many logs each side; 0 means the default.
	// Direction "before" or "after" (with Cursor) loads more of one side.
	Before int `json:"before,omitempty"`
	After  int `json:"after,omitempty"`
	// Scope: fields whose anchor values the context must share, e.g.
	// ["k8s_pod_name"] or ["trace_id"]. Omitted (null) means the anchor's
	// workload (AutoContextScope); [] means every log in the project.
	Scope []string `json:"scope"`
	// DefaultExtraFields: added to the automatic scope; set by the HTTP
	// service from LOG_CONTEXT_DEFAULT_FIELDS.
	DefaultExtraFields []string `json:"-"`
	// Query: an extra filter on the context, optional.
	Query *QueryNode `json:"query,omitempty"`
	// WindowMinutes: how far each side of the anchor to look; 0 means the
	// default.
	WindowMinutes int `json:"windowMinutes,omitempty"`
	// Direction and Cursor load more of one side: "before" continues
	// before Cursor (the response's beforeCursor), "after" after it.
	Direction string `json:"direction,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB.
	ScanBudget int64 `json:"-"`
}

type LogContextAnchorDTO struct {
	ID        string    `json:"id"        binding:"required"`
	Timestamp time.Time `json:"timestamp" binding:"required"`
}

// LogContextScopeDTO is one field=value the context was limited to.
type LogContextScopeDTO struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type LogContextResponseDTO struct {
	// Anchor is nil when the log is gone (retention, deletion).
	Anchor *LogItemDTO `json:"anchor"`
	// Before and After are oldest first.
	Before []LogItemDTO         `json:"before"`
	After  []LogItemDTO         `json:"after"`
	Scope  []LogContextScopeDTO `json:"scope"`
	// BeforeCursor / AfterCursor load more of that side; empty when the
	// side came back short (nothing more in the window).
	BeforeCursor  string `json:"beforeCursor,omitempty"`
	AfterCursor   string `json:"afterCursor,omitempty"`
	WindowMinutes int    `json:"windowMinutes"`
	Partial       bool   `json:"partial,omitempty"`
}

// DeploysRequestDTO asks for the Deployment rollouts that began in
// TimeRange (deploy markers on the Logs histogram), among the logs matching
// Query when it filters only on level, service, namespace, container, pod
// or pattern (otherwise across the project).
type DeploysRequestDTO struct {
	Query     *QueryNode    `json:"query,omitempty"`
	TimeRange *TimeRangeDTO `json:"timeRange,omitempty"`
}

// DeployMarkerDTO is one rollout: a new ReplicaSet's pods first logged at
// Time.
type DeployMarkerDTO struct {
	Time       time.Time `json:"time"`
	Namespace  string    `json:"namespace"`
	Workload   string    `json:"workload"`
	ReplicaSet string    `json:"replicaSet"`
	Pods       int       `json:"pods"`
}

type DeploysResponseDTO struct {
	Markers []DeployMarkerDTO `json:"markers"`
}

// Pattern change kinds (PatternChangeDTO.Change).
const (
	PatternChangeNew   = "new"
	PatternChangeSpike = "spike"
	PatternChangeDrop  = "drop"
)

// PatternChangesRequestDTO compares message patterns between two ranges -
// "what changed?": patterns new in Current, spiking (at least 2x their
// Baseline count, scaled to Current's length), or dropping (at most half).
type PatternChangesRequestDTO struct {
	Query    *QueryNode    `json:"query,omitempty"`
	Current  *TimeRangeDTO `json:"current"`
	Baseline *TimeRangeDTO `json:"baseline"`
	// MinCount: fewest logs for a pattern to count as new or spiking (or,
	// scaled, as dropping); 0 means 5.
	MinCount int64 `json:"minCount,omitempty"`
	// Limit: most patterns of each kind; 0 means 50.
	Limit int `json:"limit,omitempty"`
	// ScanBudget: set by the HTTP service from QUERY_MAX_SCAN_GB.
	ScanBudget int64 `json:"-"`
}

// PatternChangeDTO is one changed pattern: its counts in each range, the
// kind of change and Ratio (Current against the scaled Baseline; a large
// number for a new pattern), plus its template and its newest log.
type PatternChangeDTO struct {
	Hash     string  `json:"hash"`
	Pattern  string  `json:"pattern"`
	Sample   string  `json:"sample"`
	Level    string  `json:"level"`
	Current  int64   `json:"current"`
	Baseline int64   `json:"baseline"`
	Change   string  `json:"change"`
	Ratio    float64 `json:"ratio"`
}

type PatternChangesResponseDTO struct {
	// Changes: new patterns (most logs first), then spikes (largest ratio
	// first), then drops (largest fall first).
	Changes       []PatternChangeDTO `json:"changes"`
	CurrentTotal  int64              `json:"currentTotal"`
	BaselineTotal int64              `json:"baselineTotal"`
}
