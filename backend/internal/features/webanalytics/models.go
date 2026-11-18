package webanalytics

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Visitor IP storage modes (Settings.IPMode).
const (
	ipModeTruncate = "truncate"
	ipModeHash     = "hash"
	ipModeFull     = "full"
)

// Rollup granularities.
const (
	granularityHour = "h"
	granularityDay  = "d"
)

// Settings is a project's Web analytics configuration. A row exists once
// someone opened Web analytics for the project, which is also what makes
// the worker summarise its hours.
type Settings struct {
	ProjectID        uuid.UUID `gorm:"column:project_id;primaryKey"`
	IPMode           string    `gorm:"column:ip_mode"`
	HashSalt         []byte    `gorm:"column:hash_salt"`
	RetentionDays    int       `gorm:"column:retention_days"`
	ExcludeBots      bool      `gorm:"column:exclude_bots"`
	StaticExtensions string    `gorm:"column:static_extensions"`
	GeoEnabled       bool      `gorm:"column:geo_enabled"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (Settings) TableName() string { return "web_analytics_settings" }

// Source is an access-log source seen in a project, with the name and
// merge the settings give it.
type Source struct {
	ProjectID    uuid.UUID       `gorm:"column:project_id;primaryKey"`
	SourceKey    string          `gorm:"column:source_key;primaryKey"`
	Name         string          `gorm:"column:name"`
	MergedInto   *string         `gorm:"column:merged_into"`
	IdentifiedBy string          `gorm:"column:identified_by"`
	Identity     json.RawMessage `gorm:"column:identity;type:jsonb"`
	Format       string          `gorm:"column:format"`
	FirstSeen    time.Time       `gorm:"column:first_seen"`
	LastSeen     time.Time       `gorm:"column:last_seen"`
}

func (Source) TableName() string { return "web_analytics_sources" }

// Build marks a bucket as summarised.
type Build struct {
	ProjectID   uuid.UUID `gorm:"column:project_id;primaryKey"`
	Granularity string    `gorm:"column:granularity;primaryKey"`
	BucketStart time.Time `gorm:"column:bucket_start;primaryKey"`
	BuiltAt     time.Time `gorm:"column:built_at"`
	DurationMs  int64     `gorm:"column:duration_ms"`
	RowsRead    int64     `gorm:"column:rows_read"`
}

func (Build) TableName() string { return "web_analytics_builds" }

// TotalsRow is one source and host's counts for an hour or a day.
type TotalsRow struct {
	ProjectID    uuid.UUID `gorm:"column:project_id;primaryKey"`
	Granularity  string    `gorm:"column:granularity;primaryKey"`
	BucketStart  time.Time `gorm:"column:bucket_start;primaryKey"`
	SourceKey    string    `gorm:"column:source_key;primaryKey"`
	Host         string    `gorm:"column:host;primaryKey"`
	Format       string    `gorm:"column:format"`
	Requests     int64     `gorm:"column:requests"`
	Bytes        int64     `gorm:"column:bytes"`
	Status2xx    int64     `gorm:"column:status_2xx"`
	Status3xx    int64     `gorm:"column:status_3xx"`
	Status4xx    int64     `gorm:"column:status_4xx"`
	Status5xx    int64     `gorm:"column:status_5xx"`
	StatusOther  int64     `gorm:"column:status_other"`
	NotFound     int64     `gorm:"column:not_found"`
	BotRequests  int64     `gorm:"column:bot_requests"`
	DurationHist string    `gorm:"column:duration_hist"`
	WithClient   int64     `gorm:"column:with_client"`
	WithBytes    int64     `gorm:"column:with_bytes"`
	WithReferer  int64     `gorm:"column:with_referer"`
	WithUA       int64     `gorm:"column:with_ua"`
	WithDuration int64     `gorm:"column:with_duration"`
	WithTLS      int64     `gorm:"column:with_tls"`
	WithHost     int64     `gorm:"column:with_host"`
	WithProtocol int64     `gorm:"column:with_protocol"`
	VisitorsHLL  []byte    `gorm:"column:visitors_hll"`
}

func (TotalsRow) TableName() string { return "web_analytics_totals" }

// KeyRow is one top-list row of a source and host for an hour or a day.
type KeyRow struct {
	ProjectID   uuid.UUID  `gorm:"column:project_id;primaryKey"`
	Granularity string     `gorm:"column:granularity;primaryKey"`
	BucketStart time.Time  `gorm:"column:bucket_start;primaryKey"`
	SourceKey   string     `gorm:"column:source_key;primaryKey"`
	Host        string     `gorm:"column:host;primaryKey"`
	Dim         int16      `gorm:"column:dim;primaryKey"`
	Key         string     `gorm:"column:key;primaryKey"`
	Hits        int64      `gorm:"column:hits"`
	Bytes       int64      `gorm:"column:bytes"`
	Errors5xx   int64      `gorm:"column:errors_5xx"`
	Referred    int64      `gorm:"column:referred"`
	LastSeen    *time.Time `gorm:"column:last_seen"`
	Extra       string     `gorm:"column:extra"`
	VisitorsHLL []byte     `gorm:"column:visitors_hll"`
}

func (KeyRow) TableName() string { return "web_analytics_keys" }

// totalsRowOf is the stored form of one totals aggregate.
func totalsRowOf(projectID uuid.UUID, granularity string, key bucketKey, totals *totalsAgg) TotalsRow {
	return TotalsRow{
		ProjectID: projectID, Granularity: granularity, BucketStart: key.bucket.UTC(), SourceKey: key.src,
		Host: key.host, Format: totals.format, Requests: totals.requests, Bytes: totals.bytes,
		Status2xx: totals.s2, Status3xx: totals.s3, Status4xx: totals.s4, Status5xx: totals.s5,
		StatusOther: totals.sOther, NotFound: totals.notFound, BotRequests: totals.bots,
		DurationHist: encodeHist(totals.hist), WithClient: totals.with[withClient], WithBytes: totals.with[withBytes],
		WithReferer: totals.with[withReferer], WithUA: totals.with[withUA], WithDuration: totals.with[withDuration],
		WithTLS: totals.with[withTLS], WithHost: totals.with[withHost], WithProtocol: totals.with[withProtocol],
		VisitorsHLL: totals.visitors.encode(),
	}
}

// aggregate reads a stored totals row back; a malformed sketch counts as
// no visitors rather than failing the report.
func (row TotalsRow) aggregate() *totalsAgg {
	totals := newTotals()
	totals.requests, totals.bytes = row.Requests, row.Bytes
	totals.s2, totals.s3, totals.s4, totals.s5, totals.sOther = row.Status2xx, row.Status3xx, row.Status4xx,
		row.Status5xx, row.StatusOther
	totals.notFound, totals.bots = row.NotFound, row.BotRequests
	totals.with = [withCount]int64{row.WithClient, row.WithBytes, row.WithReferer, row.WithUA, row.WithDuration,
		row.WithTLS, row.WithHost, row.WithProtocol}
	totals.hist = decodeHist(row.DurationHist)
	if visitors, err := decodeSketch(row.VisitorsHLL, totalPrecision); err == nil {
		totals.visitors = visitors
	}
	totals.format, totals.formatWeight = row.Format, row.Requests
	return totals
}

func keyRowOf(projectID uuid.UUID, granularity string, bucket time.Time, sh srcHost, dim int, key string,
	agg *keyAgg) KeyRow {
	row := KeyRow{
		ProjectID: projectID, Granularity: granularity, BucketStart: bucket.UTC(), SourceKey: sh.src, Host: sh.host,
		Dim: int16(dim), Key: strings.ReplaceAll(key, "\x00", ""), Hits: agg.hits, Bytes: agg.bytes, Errors5xx: agg.e5,
		Referred: agg.referred, Extra: agg.extra,
	}
	if !agg.lastSeen.IsZero() {
		lastSeen := agg.lastSeen.UTC()
		row.LastSeen = &lastSeen
	}
	if agg.visitors != nil {
		row.VisitorsHLL = agg.visitors.encode()
	}
	return row
}

func (row KeyRow) aggregate() *keyAgg {
	agg := &keyAgg{hits: row.Hits, bytes: row.Bytes, e5: row.Errors5xx, referred: row.Referred, extra: row.Extra}
	if row.LastSeen != nil {
		agg.lastSeen = row.LastSeen.UTC()
	}
	if sketchDims[int(row.Dim)] {
		if visitors, err := decodeSketch(row.VisitorsHLL, keyPrecision); err == nil {
			agg.visitors = visitors
		}
	}
	return agg
}

func encodeHist(hist []int64) string {
	parts := make([]string, len(hist))
	allZero := true
	for i, count := range hist {
		parts[i] = strconv.FormatInt(count, 10)
		if count != 0 {
			allZero = false
		}
	}
	if allZero {
		return ""
	}
	return strings.Join(parts, ",")
}

func decodeHist(text string) []int64 {
	hist := make([]int64, len(durationBoundsMs)+1)
	if text == "" {
		return hist
	}
	for i, part := range strings.Split(text, ",") {
		if i >= len(hist) {
			break
		}
		hist[i], _ = strconv.ParseInt(part, 10, 64)
	}
	return hist
}
