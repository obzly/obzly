package segments

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// MetricPartialsStream holds, per project and hour, each series' samples
// folded into 5-minute buckets that are closed on the right, (end - 5m,
// end], with what a PromQL range function needs to evaluate a window made
// of whole buckets exactly: count, first and last samples, the counter
// resets inside the bucket, sum, min, max (NaN skipped), whether two
// samples share a millisecond, and the latest stale marker (left out of
// the rest, as range vectors leave it out). PromQL windows are (t - range,
// t], so a window whose t and range are multiples of 5 m is a union of
// these buckets; the left-closed metrics_5m/metrics_1h rollups never are.
//
// An hour is folded from every metrics segment overlapping it at once:
// segments overlap in time (batches arrive out of order across flushes),
// so one series' samples interleave across them, and per-segment buckets
// couldn't be merged exactly (a counter reset between two segments'
// samples is invisible). Its stats.source lists those segments
// (MetricPartialsSourceKey); once that set changes - a merge, late samples
// in a new L0 - the hour is stale and read from samples until rebuilt.
const MetricPartialsStream = "metrics_partials_5m"

// MetricPartialsWidth is MetricPartialsStream's bucket width.
const MetricPartialsWidth = 5 * time.Minute

// MetricPartials1mStream is MetricPartialsStream with 1-minute buckets,
// folded alongside it from the same segments: a Grafana-style 1 m step
// over 24 h is whole 1-minute buckets, never whole 5-minute ones.
const MetricPartials1mStream = "metrics_partials_1m"

// MetricPartials1mWidth is MetricPartials1mStream's bucket width.
const MetricPartials1mWidth = time.Minute

// DefaultMetricPartials1mDays is METRICS_PARTIALS_1M_DAYS when unset.
const DefaultMetricPartials1mDays = 7

// metricPartials1mDays: how many days 1-minute partials are kept (0: not
// built), set from METRICS_PARTIALS_1M_DAYS (SetMetricPartials1mDays).
var metricPartials1mDays atomic.Int64

// SetMetricPartials1mDays sets how many days of 1-minute partials are kept
// and built (0: none).
func SetMetricPartials1mDays(days int) {
	metricPartials1mDays.Store(int64(max(days, 0)))
}

// MetricPartialTiersFor is the tiers the period ending at periodEnd has at
// now: the 5-minute one always, the 1-minute one while it is younger than
// the 1-minute retention. A 1 m step is for short ranges; longer ones step
// coarser, and the 1-minute tier is the costly one (about half the
// samples' rows).
func MetricPartialTiersFor(periodEnd, now time.Time) []MetricPartialTier {
	if periodEnd.After(MetricPartials1mCutoff(now)) {
		return MetricPartialTiers
	}
	return MetricPartialTiers[:1]
}

// MetricPartials1mCutoff: 1-minute partials of periods ending at or before
// it are expired - all of them when the tier is off.
func MetricPartials1mCutoff(now time.Time) time.Time {
	days := metricPartials1mDays.Load()
	if days == 0 {
		return now.AddDate(100, 0, 0)
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

// MetricPartialTier is one partials stream and its bucket width.
type MetricPartialTier struct {
	Stream string
	Width  time.Duration
}

// MetricPartialTiers, coarsest first.
var MetricPartialTiers = []MetricPartialTier{
	{MetricPartialsStream, MetricPartialsWidth},
	{MetricPartials1mStream, MetricPartials1mWidth},
}

// MetricPartialsPeriod is the time one MetricPartialsStream segment
// covers, (end - period, end].
const MetricPartialsPeriod = time.Hour

// metricPartialsChunkRows: samples folded at a time. One lag window over
// a whole 42M-sample segment ran the compactor's 1 GB DuckDB out of
// memory, as did carrying the labels through the sort or collecting each
// bucket's values in a list.
const metricPartialsChunkRows = 2_000_000

// metricPartialsChunkSQL folds the samples in (from, to] - whole buckets -
// per series id and bucket. A sample's reset is the previous value when it
// dropped (a NaN previous value is no drop), as Prometheus counts them; a
// bucket's resets leave out its first sample's, which pairs with the
// bucket before. A sample repeated with its timestamp and value (a batch
// redelivered into another segment) counts once. %[1]s series id, %[2]d
// width ms, %[3]s source.
const metricPartialsChunkSQL = `SELECT series_id, bucket_end,
	count(*) FILTER (WHERE NOT stale AND NOT repeat) AS n,
	min(t) FILTER (WHERE NOT stale) AS first_t, arg_min(value, t) FILTER (WHERE NOT stale) AS first_v,
	max(t) FILTER (WHERE NOT stale) AS last_t, arg_max(value, t) FILTER (WHERE NOT stale) AS last_v,
	coalesce(sum(reset) FILTER (WHERE NOT stale) - arg_min(reset, t) FILTER (WHERE NOT stale), 0)::DOUBLE AS resets,
	sum(value) FILTER (WHERE NOT stale AND NOT repeat) AS sum, min(value) FILTER (WHERE NOT stale) AS min,
	max(CASE WHEN isnan(value) THEN NULL ELSE value END) FILTER (WHERE NOT stale) AS max,
	bool_or(dup) AS dup, max(t) FILTER (WHERE stale) AS stale_t
FROM (
	SELECT series_id, t, value, stale, (t + %[2]d - 1) // %[2]d * %[2]d AS bucket_end,
		CASE WHEN value < lag(value) OVER w AND NOT isnan(lag(value) OVER w) THEN lag(value) OVER w ELSE 0 END AS reset,
		coalesce(t = lag(t) OVER w AND value IS DISTINCT FROM lag(value) OVER w, false) AS dup,
		coalesce(t = lag(t) OVER w AND value IS NOT DISTINCT FROM lag(value) OVER w, false) AS repeat
	FROM (
		SELECT %[1]s AS series_id, epoch_ms(timestamp) AS t, value,
			CASE WHEN isnan(value) THEN obzly_is_stale(value) ELSE false END AS stale
		FROM %[3]s WHERE timestamp > ? AND timestamp <= ?
	)
	WINDOW w AS (PARTITION BY series_id, stale ORDER BY t)
)
GROUP BY series_id, bucket_end`

// metricPartialsCoarsenSQL folds a chunk's 1-minute buckets (read_parquet)
// into %[1]d-ms buckets, exactly as folding its samples would: counts and
// sums add, the earliest first and latest last win, and a drop from one
// non-empty bucket's last value to the next one's first is a reset, except
// into a coarse bucket's first (that pair crosses from the bucket before).
const metricPartialsCoarsenSQL = `SELECT series_id, (bucket_end + %[1]d - 1) // %[1]d * %[1]d AS bucket_end,
	sum(n)::BIGINT AS n, min(first_t) AS first_t, arg_min(first_v, first_t) AS first_v,
	max(last_t) AS last_t, arg_max(last_v, last_t) AS last_v,
	(sum(resets) + coalesce(sum(drop), 0) - coalesce(arg_min(drop, first_t), 0))::DOUBLE AS resets,
	sum(sum) AS sum, min(min) AS min, max(max) AS max, bool_or(dup) AS dup, max(stale_t) AS stale_t
FROM (
	SELECT *, CASE WHEN n = 0 THEN NULL
		WHEN first_v < lag(last_v) OVER w AND NOT isnan(lag(last_v) OVER w) THEN lag(last_v) OVER w ELSE 0 END AS drop
	FROM read_parquet(?) WINDOW w AS (PARTITION BY series_id, n > 0 ORDER BY bucket_end)
)
GROUP BY series_id, 2`

// metricPartialsSQL joins the chunks (%[2]s) with each series' labels
// from the sources (%[3]s, the period's samples only); the bucket's end is
// stored as its timestamp.
const metricPartialsSQL = `WITH l AS (
	SELECT %[1]s AS series_id, any_value(project_id) AS project_id, any_value(metric_name) AS metric_name,
		any_value(labels) AS labels, max_by(type, timestamp) AS type
	FROM %[3]s WHERE timestamp > ? AND timestamp <= ? GROUP BY 1
)
SELECT l.project_id, a.series_id, l.metric_name, l.labels, l.type,
	make_timestamp(a.bucket_end * 1000)::TIMESTAMP_NS AS timestamp,
	n, first_t, first_v, last_t, last_v, resets, sum, min, max, dup, stale_t
FROM %[2]s a JOIN l USING (series_id)
ORDER BY metric_name, series_id, timestamp`

func init() {
	SetMetricPartials1mDays(DefaultMetricPartials1mDays)
	for _, tier := range MetricPartialTiers {
		statsColumnsByStream[tier.Stream] = []statsColumn{metricsStat}
		PartitionedStreams = append(PartitionedStreams, tier.Stream)
	}
}

// MetricPartialsPeriodEnd is the end of the period holding time t (ms):
// periods are closed on the right, like buckets.
func MetricPartialsPeriodEnd(t int64) int64 {
	period := MetricPartialsPeriod.Milliseconds()
	return (t + period - 1) / period * period
}

// MetricPartialsSourceKey is stats.source of a period's partials: the ids
// of the segments it was folded from, sorted.
func MetricPartialsSourceKey(sources []*SegmentRecord) string {
	ids := make([]string, len(sources))
	for i, source := range sources {
		ids[i] = source.ID.String()
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// BuildMetricPartials writes the period ending at periodEnd of each of
// tiers (MetricPartialTiers or its first) from sources, every metrics
// segment overlapping it (all local): samples fold into 1-minute buckets,
// those into 5-minute ones - or straight into 5-minute ones without the
// 1-minute tier. Each segment's times are the period's first and last
// bucket ends, so the catalog plans it for the whole period; none when the
// period has no samples.
func (c *Compactor) BuildMetricPartials(
	projectID uuid.UUID, sources []*SegmentRecord, periodEnd time.Time, dataDir string, tiers []MetricPartialTier,
) ([]*Segment, error) {
	files := make([]ResolvedFile, len(sources))
	var rows int64
	periodStart := periodEnd.Add(-MetricPartialsPeriod)
	for i, source := range sources {
		files[i] = ResolvedFile{Path: source.Path, Legacy: source.IsLegacy()}
		rows += overlappingRows(source, periodStart, periodEnd)
	}
	source, args := SourceExpr("metrics", files)
	present, err := columnsOf(c.db, source, args)
	if err != nil {
		return nil, err
	}
	seriesID := "obzly_series_id(metric_name, labels)"
	if present["series_id"] {
		seriesID = "coalesce(series_id, " + seriesID + ")"
	}
	failed := func(err error) error {
		return fmt.Errorf("failed to build metric partials of %s: %w", periodEnd.Format(time.RFC3339), err)
	}

	scratch := filepath.Join(dataDir, MetricPartialsStream, projectID.String())
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create metric partials directory: %w", err)
	}
	name := fmt.Sprintf("%d_%s", time.Now().UnixNano(), uuid.NewString()[:8])
	var temporary []string
	defer func() {
		for _, path := range temporary {
			_ = os.Remove(path)
		}
	}()
	// Chunks are whole coarse buckets, so each folds into both tiers alone.
	fine, coarse := MetricPartials1mWidth.Milliseconds(), MetricPartialsWidth.Milliseconds()
	withFine := len(tiers) > 1
	chunks := map[string][]string{}
	for i, window := range metricPartialsChunks(periodStart.UnixMilli(), periodEnd.UnixMilli(), coarse, rows) {
		fineChunk := filepath.Join(scratch, fmt.Sprintf("%s.chunk%d.1m.parquet", name, i))
		coarseChunk := filepath.Join(scratch, fmt.Sprintf("%s.chunk%d.5m.parquet", name, i))
		temporary = append(temporary, fineChunk, coarseChunk)
		chunkArgs := append(append([]any{}, args...), time.UnixMilli(window[0]).UTC(), time.UnixMilli(window[1]).UTC())
		if !withFine {
			query := fmt.Sprintf(metricPartialsChunkSQL, seriesID, coarse, source)
			if err := c.execGated(true, compactionCopySQL(query, coarseChunk), chunkArgs...); err != nil {
				return nil, failed(err)
			}
			chunks[MetricPartialsStream] = append(chunks[MetricPartialsStream], coarseChunk)
			continue
		}
		query := fmt.Sprintf(metricPartialsChunkSQL, seriesID, fine, source)
		if err := c.execGated(true, compactionCopySQL(query, fineChunk), chunkArgs...); err != nil {
			return nil, failed(err)
		}
		query = fmt.Sprintf(metricPartialsCoarsenSQL, coarse)
		if err := c.execGated(true, compactionCopySQL(query, coarseChunk), fineChunk); err != nil {
			return nil, failed(err)
		}
		chunks[MetricPartials1mStream] = append(chunks[MetricPartials1mStream], fineChunk)
		chunks[MetricPartialsStream] = append(chunks[MetricPartialsStream], coarseChunk)
	}

	var built []*Segment
	for _, tier := range tiers {
		segment, err := c.writeMetricPartials(projectID, sources, tier, periodEnd, dataDir, name,
			seriesID, source, args, chunks[tier.Stream])
		if err != nil {
			for _, done := range built {
				_ = os.Remove(done.Path)
			}
			return nil, failed(err)
		}
		if segment != nil {
			built = append(built, segment)
		}
	}
	return built, nil
}

// writeMetricPartials joins one tier's chunks with the series' labels
// into its segment; nil when there are no rows.
func (c *Compactor) writeMetricPartials(
	projectID uuid.UUID, sources []*SegmentRecord, tier MetricPartialTier, periodEnd time.Time,
	dataDir, name, seriesID, source string, args []any, chunks []string,
) (*Segment, error) {
	segmentDir := filepath.Join(dataDir, tier.Stream, projectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create metric partials directory: %w", err)
	}
	outputPath := filepath.Join(segmentDir, name+".parquet")
	periodStart := periodEnd.Add(-MetricPartialsPeriod)
	chunksExpr, chunkArgs := ParquetFilesExpr(chunks)
	query := fmt.Sprintf(metricPartialsSQL, seriesID, chunksExpr, source)
	queryArgs := append(append(append([]any{}, args...), periodStart, periodEnd), chunkArgs...)
	if err := c.execGated(true, compactionCopySQL(query, outputPath), queryArgs...); err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	var count int64
	if err := c.db.QueryRow("SELECT count(*) FROM read_parquet(?)", outputPath).Scan(&count); err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	if count == 0 {
		_ = os.Remove(outputPath)
		return nil, nil
	}
	stats, err := computeStats(c.db, tier.Stream, "read_parquet(?)", []any{outputPath})
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	if stats == nil {
		stats = &SegmentStats{}
	}
	stats.Source = MetricPartialsSourceKey(sources)
	info, err := os.Stat(outputPath)
	if err != nil {
		return nil, err
	}
	return &Segment{
		Path:          outputPath,
		ProjectID:     projectID,
		Stream:        tier.Stream,
		RowCount:      int(count),
		SizeBytes:     info.Size(),
		RawSizeBytes:  info.Size(),
		MinTime:       periodStart.Add(tier.Width).UTC(),
		MaxTime:       periodEnd.UTC(),
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: CurrentSchemaVersion,
		Stats:         stats,
		Level:         LevelCold,
	}, nil
}

// overlappingRows estimates record's samples in (from, to] by its average
// rate.
func overlappingRows(record *SegmentRecord, from, to time.Time) int64 {
	span := record.MaxTime.Sub(record.MinTime)
	overlap := earlierOf(record.MaxTime, to).Sub(laterOf(record.MinTime, from))
	if span <= 0 || overlap >= span {
		return int64(record.RowCount)
	}
	if overlap <= 0 {
		return 0
	}
	return int64(float64(record.RowCount) * float64(overlap) / float64(span))
}

// metricPartialsChunks cuts (fromMs, toMs] into windows of whole buckets
// holding about metricPartialsChunkRows of rows samples each.
func metricPartialsChunks(fromMs, toMs, width, rows int64) [][2]int64 {
	buckets := max((toMs-fromMs)/width, 1)
	perChunk := buckets
	if rows > metricPartialsChunkRows {
		perChunk = max(buckets*metricPartialsChunkRows/rows, 1)
	}
	var windows [][2]int64
	for from := fromMs; from < toMs; from += perChunk * width {
		windows = append(windows, [2]int64{from, min(from+perChunk*width, toMs)})
	}
	return windows
}

func earlierOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
