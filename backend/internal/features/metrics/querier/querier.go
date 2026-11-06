// Package metrics_querier reads metric samples from Parquet segments for
// PromQL selectors and label metadata (see ARCHITECTURE.md "Metrics").
// Matchers compile to DuckDB predicates so executors filter where the
// segments are; PromQL semantics hold: a missing label reads as "", and
// regexes are fully anchored RE2.
package metrics_querier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

// maxSeriesRows: a var so tests can lower it.
var maxSeriesRows = 2_000_000

// rowLimit stops a raw-row read one row past maxSeriesRows: enough to
// refuse the query, where each fragment used to write out every matching
// row first.
const rowLimit = " LIMIT ?"

// maxSeriesIDs: a larger selection reads samples by matchers - its ids
// would pass the broker's message size limit as an executor argument.
const maxSeriesIDs = 100_000

// seriesStream is metrics_ingester.StreamSeries (not imported: the ingester
// package imports nothing from here, keep it that way).
const seriesStream = "metrics_series"

type MetricQuerier struct {
	db          *sql.DB
	segmentRepo *segments.SegmentRepository

	objectStore *segments.ObjectStore
	cacheDir    string
	// tail adds rows still pending in JetStream (nil without the broker).
	tail        segments.TailSource
	coordinator *query.Coordinator
	queryDir    string
	memoryLimit string
	spillDir    string
}

type QuerierOption func(*MetricQuerier)

// WithTail makes every query include rows still pending in JetStream.
func WithTail(tail segments.TailSource) QuerierOption {
	return func(q *MetricQuerier) {
		q.tail = tail
	}
}

func WithObjectStore(store *segments.ObjectStore, cacheDir string) QuerierOption {
	return func(q *MetricQuerier) {
		q.objectStore = store
		q.cacheDir = cacheDir
	}
}

// WithQueryDir keeps query fragment results (and spilled data) in dir.
func WithQueryDir(dir string) QuerierOption {
	return func(q *MetricQuerier) {
		q.queryDir = dir
	}
}

// WithMemoryLimit caps the querier's DuckDB (see logs_querier.WithMemoryLimit).
func WithMemoryLimit(limit string) QuerierOption {
	return func(q *MetricQuerier) {
		q.memoryLimit = limit
	}
}

func NewMetricQuerier(opts ...QuerierOption) (*MetricQuerier, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb handle: %w", err)
	}
	if err := segments.RegisterNormalizationFunctions(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	querier := &MetricQuerier{
		db: db, segmentRepo: &segments.SegmentRepository{},
		queryDir: filepath.Join(os.TempDir(), "obzly-query-metrics"),
	}
	for _, opt := range opts {
		opt(querier)
	}
	querier.spillDir, err = segments.ConfigureMemory(db, querier.memoryLimit, filepath.Join(querier.queryDir, "spill"))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	local := query.NewLocalExecutor("local", db, querier.objectStore, querier.cacheDir)
	querier.coordinator, err = query.NewCoordinator(
		querier.queryDir,
		db,
		local,
		query.RemoteExecutors,
		logger.GetLogger(),
	)
	if err != nil {
		_ = db.Close()
		_ = os.RemoveAll(querier.spillDir)
		return nil, err
	}
	telemetry.RegisterDuckDB("querier_metrics", db)

	return querier, nil
}

func (q *MetricQuerier) Close() error {
	telemetry.UnregisterDuckDB(q.db)
	err := q.db.Close()
	_ = os.RemoveAll(q.spillDir)
	return err
}

// SelectSeries returns every series matching request.Matchers in the time
// range, samples ascending by timestamp.
func (q *MetricQuerier) SelectSeries(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.SelectSeriesRequestDTO,
) (*metrics_core.SelectSeriesResponseDTO, error) {
	empty := &metrics_core.SelectSeriesResponseDTO{Series: []metrics_core.SeriesDTO{}}
	from, to := resolveTimeRange(request.TimeRange)
	if covered, err := q.seriesCovered(projectID, from); err != nil {
		return nil, err
	} else if covered {
		response, ok, err := q.selectByID(ctx, projectID, from, to, request.Matchers, request.StepMs, request.RangeMs)
		if err != nil || ok {
			return response, err
		}
	}
	where, args, err := q.whereFor(projectID, from, to, request.Matchers)
	if err != nil {
		return nil, err
	}
	tail, records, err := q.inputs(projectID, from, to, nameEqualityOf(request.Matchers))
	if err != nil {
		return nil, err
	}
	var hints segments.PruningHints
	if name := nameEqualityOf(request.Matchers); name != "" {
		hints.MetricNames = []string{name}
	}
	sources, err := q.sampleSources(projectID, from, to, records, tail, request.StepMs, request.RangeMs, hints)
	if err != nil {
		return nil, err
	}

	type seriesAccumulator struct {
		metricName string
		labels     map[string]string
		samples    []metrics_core.SampleDTO
	}
	order := make([]string, 0)
	bySeries := make(map[string]*seriesAccumulator)
	ordered := " ORDER BY metric_name, labels, timestamp ASC"
	found := false
	seen := 0
	for _, source := range sources {
		rowsSQL := "SELECT metric_name, labels, timestamp, value FROM " + source.from + " WHERE " + where
		limitedArgs := append(append([]any{}, args...), maxSeriesRows+1)
		sourceFound, err := q.run(ctx, query.Request{
			Stream:       source.stream,
			Segments:     source.records,
			LocalFiles:   source.tail,
			FragmentSQL:  rowsSQL + rowLimit,
			FragmentArgs: limitedArgs,
			FinalSQL:     "SELECT * FROM " + query.ResultsPlaceholder + ordered + rowLimit,
			FinalArgs:    []any{maxSeriesRows + 1},
			SingleSQL:    rowsSQL + ordered + rowLimit,
			SingleArgs:   limitedArgs,
		}, func(rows *sql.Rows) error {
			for rows.Next() {
				var metricName, labelsJSON string
				var ts time.Time
				var value float64
				if err := rows.Scan(&metricName, &labelsJSON, &ts, &value); err != nil {
					return fmt.Errorf("failed to scan metric row: %w", err)
				}
				seen++
				if seen > maxSeriesRows {
					return fmt.Errorf(
						"query matches more than %d raw samples; narrow the time range or matchers",
						maxSeriesRows,
					)
				}
				key := metricName + "\x00" + labelsJSON
				acc, ok := bySeries[key]
				if !ok {
					acc = &seriesAccumulator{metricName: metricName, labels: decodeLabels(labelsJSON)}
					bySeries[key] = acc
					order = append(order, key)
				}
				acc.samples = append(acc.samples, metrics_core.SampleDTO{Timestamp: ts, Value: value})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		found = found || sourceFound
	}
	if !found {
		return empty, nil
	}

	sort.Strings(order)
	series := make([]metrics_core.SeriesDTO, 0, len(order))
	for _, key := range order {
		acc := bySeries[key]
		series = append(series, metrics_core.SeriesDTO{
			MetricName: acc.metricName,
			Labels:     acc.labels,
			Samples:    inTimeOrder(acc.samples),
		})
	}
	return &metrics_core.SelectSeriesResponseDTO{Series: series}, nil
}

// selectByID runs the matchers on the series stream, then reads samples by
// series id so no sample row's labels are parsed. false when the selection
// is too large to pass as ids (the caller filters samples by matchers).
func (q *MetricQuerier) selectByID(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	matchers []metrics_core.MatcherDTO,
	stepMs, rangeMs int64,
) (*metrics_core.SelectSeriesResponseDTO, bool, error) {
	matched, err := q.distinctFromSeriesStream(ctx, projectID, from, to, matchers)
	if err != nil {
		return nil, false, err
	}
	if len(matched) > maxSeriesIDs {
		return nil, false, nil
	}
	response := &metrics_core.SelectSeriesResponseDTO{Series: []metrics_core.SeriesDTO{}}
	if len(matched) == 0 {
		return response, true, nil
	}

	bySeries := make(map[uint64]*metrics_core.SeriesDTO, len(matched))
	ids := make([]string, 0, len(matched))
	nameSet := map[string]struct{}{}
	for _, row := range matched {
		if _, ok := bySeries[row.seriesID]; ok {
			continue
		}
		bySeries[row.seriesID] = &metrics_core.SeriesDTO{MetricName: row.metricName, Labels: row.labels}
		ids = append(ids, strconv.FormatUint(row.seriesID, 10))
		nameSet[row.metricName] = struct{}{}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)

	records, err := q.segmentRepo.PlanSegments(projectID, "metrics", from, to,
		segments.PruningHints{MetricNames: names})
	if err != nil {
		return nil, false, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", records, from, to, nil)
	sources, err := q.sampleSources(
		projectID,
		from,
		to,
		records,
		tail,
		stepMs,
		rangeMs,
		segments.PruningHints{MetricNames: names},
	)
	if err != nil {
		return nil, false, err
	}
	// Ids and names travel as one string each (any executor binds a string).
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ?" +
		" AND metric_name IN (SELECT unnest(string_split(?, ',')))" +
		" AND series_id IN (SELECT CAST(unnest(string_split(?, ',')) AS UBIGINT))"
	args := []any{projectID.String(), from, to, strings.Join(names, ","), strings.Join(ids, ",")}
	ordered := " ORDER BY series_id, timestamp ASC"
	seen := 0
	for _, source := range sources {
		rowsSQL := "SELECT series_id, timestamp, value FROM " + source.from + " WHERE " + where
		limitedArgs := append(append([]any{}, args...), maxSeriesRows+1)
		_, err = q.run(ctx, query.Request{
			Stream:       source.stream,
			Segments:     source.records,
			LocalFiles:   source.tail,
			FragmentSQL:  rowsSQL + rowLimit,
			FragmentArgs: limitedArgs,
			FinalSQL:     "SELECT * FROM " + query.ResultsPlaceholder + ordered + rowLimit,
			FinalArgs:    []any{maxSeriesRows + 1},
			SingleSQL:    rowsSQL + ordered + rowLimit,
			SingleArgs:   limitedArgs,
		}, func(rows *sql.Rows) error {
			for rows.Next() {
				var seriesID uint64
				var ts time.Time
				var value float64
				if err := rows.Scan(&seriesID, &ts, &value); err != nil {
					return fmt.Errorf("failed to scan metric row: %w", err)
				}
				seen++
				if seen > maxSeriesRows {
					return fmt.Errorf(
						"query matches more than %d raw samples; narrow the time range or matchers",
						maxSeriesRows,
					)
				}
				if series, ok := bySeries[seriesID]; ok {
					series.Samples = append(series.Samples, metrics_core.SampleDTO{Timestamp: ts, Value: value})
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}

	keys := make([]string, 0, len(matched))
	byKey := make(map[string]*metrics_core.SeriesDTO, len(matched))
	for _, row := range matched {
		series := bySeries[row.seriesID]
		if len(series.Samples) == 0 {
			continue
		}
		series.Samples = inTimeOrder(series.Samples)
		key := row.metricName + "\x00" + row.labelsJSON
		if _, ok := byKey[key]; !ok {
			keys = append(keys, key)
			byKey[key] = series
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		response.Series = append(response.Series, *byKey[key])
	}
	return response, true, nil
}

func (q *MetricQuerier) ListMetricNames(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *metrics_core.TimeRangeDTO,
) (*metrics_core.ListMetricNamesResponseDTO, error) {
	from, to := resolveTimeRange(timeRange)
	latest := map[string]nameType{}
	covered, err := q.seriesCovered(projectID, from)
	if err != nil {
		return nil, err
	}
	if covered {
		// The label index (no segment read), then the tail's samples.
		if err := namesFromIndex(latest, projectID, from, to); err != nil {
			return nil, err
		}
		tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", nil, from, to, nil)
		if err := q.namesInto(ctx, latest, query.Request{LocalFiles: tail},
			"timestamp", "timestamp >= ? AND timestamp <= ?", []any{projectID.String(), from, to}); err != nil {
			return nil, err
		}
	} else {
		tail, records, err := q.inputs(projectID, from, to, "")
		if err != nil {
			return nil, err
		}
		if err := q.namesInto(ctx, latest, query.Request{Segments: records, LocalFiles: tail},
			"timestamp", "timestamp >= ? AND timestamp <= ?", []any{projectID.String(), from, to}); err != nil {
			return nil, err
		}
	}

	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	metrics := make([]metrics_core.MetricNameInfo, 0, len(names))
	for _, name := range names {
		metricType := metrics_core.MetricType(latest[name].metricType)
		if !metricType.IsValid() {
			metricType = metrics_core.MetricTypeGauge
		}
		metrics = append(metrics, metrics_core.MetricNameInfo{Name: name, Type: metricType})
	}
	return &metrics_core.ListMetricNamesResponseDTO{Metrics: metrics}, nil
}

// labelNamesFromIndex answers from metric_label_index when the window is
// covered and the matchers are at most a metric-name equality: the index is
// per metric, not per series, so any other matcher needs the series stream.
func (q *MetricQuerier) labelNamesFromIndex(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	wholeIndex bool,
	matchers []metrics_core.MatcherDTO,
) ([]string, bool, error) {
	metricName, ok := indexableMatchers(matchers)
	if !ok {
		return nil, false, nil
	}
	if covered, err := q.indexCovers(projectID, from, wholeIndex); err != nil || !covered {
		return nil, false, err
	}
	where, args := indexWindow(projectID, from, to, metricName)
	var fromIndex []string
	if err := storage.GetDb().Raw("SELECT DISTINCT label_name FROM metric_label_index WHERE "+where, args...).
		Scan(&fromIndex).Error; err != nil {
		return nil, false, fmt.Errorf("failed to read metric label index: %w", err)
	}
	set := map[string]struct{}{"__name__": {}}
	for _, name := range fromIndex {
		set[name] = struct{}{}
	}
	tail, err := q.tailSeries(ctx, projectID, from, to, matchers)
	if err != nil {
		return nil, false, err
	}
	for _, row := range tail {
		for name := range row.labels {
			set[name] = struct{}{}
		}
	}
	return sortedKeys(set), true, nil
}

// labelValuesFromIndex is labelNamesFromIndex for one label's values.
func (q *MetricQuerier) labelValuesFromIndex(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	wholeIndex bool,
	labelName string,
	matchers []metrics_core.MatcherDTO,
) ([]string, bool, error) {
	metricName, ok := indexableMatchers(matchers)
	if !ok {
		return nil, false, nil
	}
	if covered, err := q.indexCovers(projectID, from, wholeIndex); err != nil || !covered {
		return nil, false, err
	}
	where, args := indexWindow(projectID, from, to, metricName)
	sqlText := "SELECT DISTINCT label_value FROM metric_label_index WHERE " + where + " AND label_name = ?"
	args = append(args, labelName)
	if labelName == "__name__" {
		sqlText = "SELECT DISTINCT metric_name FROM metric_name_index WHERE " + where
		args = args[:len(args)-1]
	}
	var fromIndex []string
	if err := storage.GetDb().Raw(sqlText, args...).Scan(&fromIndex).Error; err != nil {
		return nil, false, fmt.Errorf("failed to read metric label index: %w", err)
	}
	set := map[string]struct{}{}
	for _, value := range fromIndex {
		set[value] = struct{}{}
	}
	tail, err := q.tailSeries(ctx, projectID, from, to, matchers)
	if err != nil {
		return nil, false, err
	}
	for _, row := range tail {
		if labelName == "__name__" {
			set[row.metricName] = struct{}{}
		} else if value, ok := row.labels[labelName]; ok {
			set[value] = struct{}{}
		}
	}
	return sortedKeys(set), true, nil
}

// tailSeries covers series not yet in the index or the series stream.
func (q *MetricQuerier) tailSeries(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	matchers []metrics_core.MatcherDTO,
) ([]distinctSeriesRow, error) {
	tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", nil, from, to, nil)
	if len(tail) == 0 {
		return nil, nil
	}
	where, args, err := q.whereFor(projectID, from, to, matchers)
	if err != nil {
		return nil, err
	}
	return q.distinctWithIDs(ctx, query.Request{LocalFiles: tail}, where, args)
}

func namesFromIndex(latest map[string]nameType, projectID uuid.UUID, from, to time.Time) error {
	var rows []struct {
		MetricName string
		MetricType string
		LastSeen   time.Time
	}
	where, args := indexWindow(projectID, from, to, "")
	if err := storage.GetDb().Raw("SELECT metric_name, metric_type, last_seen FROM metric_name_index WHERE "+where,
		args...).Scan(&rows).Error; err != nil {
		return fmt.Errorf("failed to read metric name index: %w", err)
	}
	for _, row := range rows {
		if current, ok := latest[row.MetricName]; !ok || row.LastSeen.After(current.at) {
			latest[row.MetricName] = nameType{metricType: row.MetricType, at: row.LastSeen}
		}
	}
	return nil
}

// indexableMatchers: no matcher, or one metric-name equality (its name).
func indexableMatchers(matchers []metrics_core.MatcherDTO) (string, bool) {
	switch {
	case len(matchers) == 0:
		return "", true
	case len(matchers) == 1 && matchers[0].Name == "__name__" && matchers[0].Type == metrics_core.MatchEqual:
		return matchers[0].Value, true
	default:
		return "", false
	}
}

func indexWindow(projectID uuid.UUID, from, to time.Time, metricName string) (string, []any) {
	where := "project_id = ? AND first_seen <= ? AND last_seen >= ?"
	args := []any{projectID, to, from}
	if metricName != "" {
		where += " AND metric_name = ?"
		args = append(args, metricName)
	}
	return where, args
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sampleSource is files to read samples from: raw samples, or a rollup
// stream presented as samples (from wraps SourcePlaceholder).
type sampleSource struct {
	stream  string
	records []*segments.SegmentRecord
	tail    []segments.ResolvedFile
	from    string
}

// rollupPoints presents a rollup stream as samples: each bucket's first,
// min, max and last points.
const rollupPoints = "(SELECT project_id, series_id, metric_name, labels, " +
	"unnest([first_ts, min_ts, max_ts, last_ts]) AS timestamp, " +
	"unnest([first_value, min_value, max_value, last_value]) AS value FROM " + query.SourcePlaceholder + ")"

// sampleSources is where a select reads: everything raw for a fine step;
// for a step of at least a rollup's bucket, the rollups of the cold
// segments in the window and raw samples for the rest (segments not rolled
// up yet, and the tail). Aged rollups (samples retired by retention) are
// read at any step, the finest (5 m) for a fine one; any other rollup whose
// cold segment is gone is ignored.
func (q *MetricQuerier) sampleSources(
	projectID uuid.UUID,
	from, to time.Time,
	records []*segments.SegmentRecord,
	tail []segments.ResolvedFile,
	stepMs, rangeMs int64,
	hints segments.PruningHints,
) ([]sampleSource, error) {
	raw := sampleSource{stream: "metrics", records: records, tail: tail, from: query.SourcePlaceholder}
	stream := rollupFor(stepMs, rangeMs)
	if stream == "" {
		stream = "metrics_5m"
	}
	rollups, err := q.segmentRepo.PlanSegments(projectID, stream, from, to, hints)
	if err != nil {
		return nil, fmt.Errorf("failed to list rollup segments for project: %w", err)
	}
	planned := make(map[string]bool, len(records))
	for _, record := range records {
		planned[record.ID.String()] = true
	}
	rolled := sampleSource{stream: stream, from: rollupPoints}
	covered := map[string]bool{}
	coarse := rollupFor(stepMs, rangeMs) != ""
	for _, rollup := range rollups {
		switch {
		case rollup.Stats == nil:
		case rollup.Stats.Aged && !planned[rollup.Stats.Source]:
			// (Still planned: its retirement failed - read the samples.)
			rolled.records = append(rolled.records, rollup)
		case coarse && planned[rollup.Stats.Source]:
			rolled.records = append(rolled.records, rollup)
			covered[rollup.Stats.Source] = true
		}
	}
	if len(rolled.records) == 0 {
		return []sampleSource{raw}, nil
	}
	raw.records = nil
	for _, record := range records {
		if !covered[record.ID.String()] {
			raw.records = append(raw.records, record)
		}
	}
	return []sampleSource{raw, rolled}, nil
}

// minBucketsPerRange: a range function (rate(x[1h])) reads a rollup only
// if its window spans this many buckets. PromQL extrapolates rate at a
// series' edges by half the average sample interval, which on rollups is a
// bucket's: rate(x[1h]) over 1 h buckets read up to 43% high (5 m: <6%).
const minBucketsPerRange = 5

// rollupFor is the coarsest rollup stream whose bucket fits in stepMs and,
// for a range function, minBucketsPerRange times in rangeMs ("": raw
// samples).
func rollupFor(stepMs, rangeMs int64) string {
	step, window := time.Duration(stepMs)*time.Millisecond, time.Duration(rangeMs)*time.Millisecond
	for _, stream := range []string{"metrics_1h", "metrics_5m"} {
		width := segments.RollupStreams[stream]
		if step >= width && (window == 0 || window >= minBucketsPerRange*width) {
			return stream
		}
	}
	return ""
}

// inTimeOrder sorts samples and drops repeats of a timestamp (a rollup
// bucket's first point may also be its min, or a sample may come from two
// sources).
func inTimeOrder(samples []metrics_core.SampleDTO) []metrics_core.SampleDTO {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].Timestamp.Before(samples[j].Timestamp) })
	result := samples[:0]
	for i, sample := range samples {
		if i > 0 && sample.Timestamp.Equal(samples[i-1].Timestamp) {
			continue
		}
		result = append(result, sample)
	}
	return result
}

type nameType struct {
	metricType string
	at         time.Time
}

// namesInto adds each metric name's latest type among request's files to
// latest (timeColumn orders them, timeWhere bounds them).
func (q *MetricQuerier) namesInto(
	ctx context.Context,
	latest map[string]nameType,
	request query.Request,
	timeColumn, timeWhere string,
	args []any,
) error {
	where := " WHERE project_id = ? AND " + timeWhere + " GROUP BY metric_name"
	request.FragmentSQL = "SELECT metric_name, max_by(type, " + timeColumn + ") AS latest_type, max(" +
		timeColumn + ") AS latest FROM " + query.SourcePlaceholder + where
	request.FragmentArgs = args
	request.FinalSQL = "SELECT metric_name, max_by(latest_type, latest), max(latest) FROM " +
		query.ResultsPlaceholder + " GROUP BY metric_name"
	request.SingleSQL = request.FragmentSQL
	request.SingleArgs = args
	_, err := q.run(ctx, request, func(rows *sql.Rows) error {
		for rows.Next() {
			var name, metricType string
			var at time.Time
			if err := rows.Scan(&name, &metricType, &at); err != nil {
				return fmt.Errorf("failed to scan metric name row: %w", err)
			}
			if current, ok := latest[name]; !ok || at.After(current.at) {
				latest[name] = nameType{metricType: metricType, at: at}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list metric names: %w", err)
	}
	return nil
}

func (q *MetricQuerier) ListLabelNames(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.ListLabelNamesRequestDTO,
) (*metrics_core.ListLabelNamesResponseDTO, error) {
	from, to, wholeIndex := labelLookupRange(request.TimeRange)
	if names, ok, err := q.labelNamesFromIndex(ctx, projectID, from, to, wholeIndex, request.Matchers); err != nil ||
		ok {
		if err != nil {
			return nil, err
		}
		return &metrics_core.ListLabelNamesResponseDTO{Names: names}, nil
	}
	distinctRows, err := q.fetchDistinctSeries(ctx, projectID, from, to, wholeIndex, request.Matchers)
	if err != nil {
		return nil, err
	}

	nameSet := map[string]struct{}{"__name__": {}}
	for _, row := range distinctRows {
		for key := range row.labels {
			nameSet[key] = struct{}{}
		}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return &metrics_core.ListLabelNamesResponseDTO{Names: names}, nil
}

func (q *MetricQuerier) ListLabelValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.ListLabelValuesRequestDTO,
) (*metrics_core.ListLabelValuesResponseDTO, error) {
	from, to, wholeIndex := labelLookupRange(request.TimeRange)
	if values, ok, err := q.labelValuesFromIndex(ctx, projectID, from, to, wholeIndex, request.LabelName,
		request.Matchers); err != nil || ok {
		if err != nil {
			return nil, err
		}
		return &metrics_core.ListLabelValuesResponseDTO{Values: values}, nil
	}
	distinctRows, err := q.fetchDistinctSeries(ctx, projectID, from, to, wholeIndex, request.Matchers)
	if err != nil {
		return nil, err
	}

	valueSet := make(map[string]struct{})
	for _, row := range distinctRows {
		if request.LabelName == "__name__" {
			valueSet[row.metricName] = struct{}{}
			continue
		}
		if value, ok := row.labels[request.LabelName]; ok {
			valueSet[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(valueSet))
	for value := range valueSet {
		values = append(values, value)
	}
	sort.Strings(values)
	return &metrics_core.ListLabelValuesResponseDTO{Values: values}, nil
}

type distinctSeriesRow struct {
	metricName string
	labels     map[string]string
	// seriesID is set by the series stream path (0 from samples).
	seriesID   uint64
	labelsJSON string
}

// fetchDistinctSeries: wholeIndex as in labelLookupRange.
func (q *MetricQuerier) fetchDistinctSeries(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	wholeIndex bool,
	matchers []metrics_core.MatcherDTO,
) ([]distinctSeriesRow, error) {
	if covered, err := q.indexCovers(projectID, from, wholeIndex); err != nil {
		return nil, err
	} else if covered {
		return q.distinctFromSeriesStream(ctx, projectID, from, to, matchers)
	}
	where, args, err := q.whereFor(projectID, from, to, matchers)
	if err != nil {
		return nil, err
	}
	tail, records, err := q.inputs(projectID, from, to, nameEqualityOf(matchers))
	if err != nil {
		return nil, err
	}
	return q.distinctFromSamples(ctx, tail, records, where, args)
}

func (q *MetricQuerier) distinctFromSamples(
	ctx context.Context,
	tail []segments.ResolvedFile,
	records []*segments.SegmentRecord,
	where string,
	args []any,
) ([]distinctSeriesRow, error) {
	distinct := "SELECT DISTINCT metric_name, labels FROM " + query.SourcePlaceholder + " WHERE " + where
	result := make([]distinctSeriesRow, 0)
	_, err := q.run(ctx, query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  distinct,
		FragmentArgs: args,
		FinalSQL:     "SELECT DISTINCT metric_name, labels FROM " + query.ResultsPlaceholder,
		SingleSQL:    distinct,
		SingleArgs:   args,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var metricName, labelsJSON string
			if err := rows.Scan(&metricName, &labelsJSON); err != nil {
				return fmt.Errorf("failed to scan distinct series row: %w", err)
			}
			result = append(result, distinctSeriesRow{metricName: metricName, labels: decodeLabels(labelsJSON)})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list distinct series: %w", err)
	}
	return result, nil
}

// distinctFromSeriesStream reads the series stream (one row per series),
// plus the tail's samples, which have no series rows yet.
func (q *MetricQuerier) distinctFromSeriesStream(
	ctx context.Context,
	projectID uuid.UUID,
	from, to time.Time,
	matchers []metrics_core.MatcherDTO,
) ([]distinctSeriesRow, error) {
	clause, matcherArgs, err := matcherSQL(matchers)
	if err != nil {
		return nil, err
	}
	// A series row covers first_seen..last_seen and its file's catalog time
	// range is its rows' last_seen, so plan from `from` with no upper bound.
	var hints segments.PruningHints
	if name := nameEqualityOf(matchers); name != "" {
		hints.MetricNames = []string{name}
	}
	records, err := q.segmentRepo.PlanSegments(projectID, seriesStream, from, maxRepresentableTime, hints)
	if err != nil {
		return nil, fmt.Errorf("failed to list series segments for project: %w", err)
	}
	where := "project_id = ? AND first_seen <= ? AND last_seen >= ?"
	if clause != "" {
		where += " AND " + clause
	}
	args := append([]any{projectID.String(), to, from}, matcherArgs...)
	result, err := q.distinctWithIDs(ctx, query.Request{Stream: seriesStream, Segments: records}, where, args)
	if err != nil {
		return nil, fmt.Errorf("failed to list series: %w", err)
	}

	tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", nil, from, to, nil)
	if len(tail) == 0 {
		return result, nil
	}
	sampleWhere, sampleArgs, err := q.whereFor(projectID, from, to, matchers)
	if err != nil {
		return nil, err
	}
	fromTail, err := q.distinctWithIDs(ctx, query.Request{LocalFiles: tail}, sampleWhere, sampleArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to list tail series: %w", err)
	}
	return append(result, fromTail...), nil
}

// distinctWithIDs: request's files must all have series_id (series stream,
// tail).
func (q *MetricQuerier) distinctWithIDs(
	ctx context.Context,
	request query.Request,
	where string,
	args []any,
) ([]distinctSeriesRow, error) {
	distinct := "SELECT DISTINCT series_id, metric_name, labels FROM " + query.SourcePlaceholder + " WHERE " + where
	request.FragmentSQL = distinct
	request.FragmentArgs = args
	request.FinalSQL = "SELECT DISTINCT series_id, metric_name, labels FROM " + query.ResultsPlaceholder
	request.SingleSQL = distinct
	request.SingleArgs = args
	result := make([]distinctSeriesRow, 0)
	_, err := q.run(ctx, request, func(rows *sql.Rows) error {
		for rows.Next() {
			var row distinctSeriesRow
			if err := rows.Scan(&row.seriesID, &row.metricName, &row.labelsJSON); err != nil {
				return fmt.Errorf("failed to scan series row: %w", err)
			}
			row.labels = decodeLabels(row.labelsJSON)
			result = append(result, row)
		}
		return nil
	})
	return result, err
}

// seriesCovered: projectID's series stream holds every series of samples
// at or after from (series tracking started no later than from).
func (q *MetricQuerier) seriesCovered(projectID uuid.UUID, from time.Time) (bool, error) {
	since, ok, err := coverageSince(projectID)
	return ok && !from.Before(since), err
}

func coverageSince(projectID uuid.UUID) (time.Time, bool, error) {
	var since []time.Time
	err := storage.GetDb().Raw("SELECT since FROM metric_series_coverage WHERE project_id = ?", projectID).
		Scan(&since).Error
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to read metric series coverage: %w", err)
	}
	if len(since) != 1 {
		return time.Time{}, false, nil
	}
	return since[0], true, nil
}

// labelLookupRange also reports whether the label index answers the lookup
// whole: without a start ("all time", e.g. a dashboard filter) it reads only
// the index, since scanning every segment took seconds per dropdown. Values
// seen only before the index existed are then left out.
func labelLookupRange(timeRange *metrics_core.TimeRangeDTO) (time.Time, time.Time, bool) {
	from, to := resolveTimeRange(timeRange)
	return from, to, timeRange == nil || timeRange.From == nil
}

// indexCovers: wholeIndex as in labelLookupRange.
func (q *MetricQuerier) indexCovers(projectID uuid.UUID, from time.Time, wholeIndex bool) (bool, error) {
	if wholeIndex {
		_, ok, err := coverageSince(projectID)
		return ok, err
	}
	return q.seriesCovered(projectID, from)
}

func (q *MetricQuerier) whereFor(
	projectID uuid.UUID,
	from, to time.Time,
	matchers []metrics_core.MatcherDTO,
) (string, []any, error) {
	clause, args, err := matcherSQL(matchers)
	if err != nil {
		return "", nil, err
	}
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	if clause != "" {
		where += " AND " + clause
	}
	return where, append([]any{projectID.String(), from, to}, args...), nil
}

// inputs: a metric name equality lets the catalog skip segments without
// that metric.
func (q *MetricQuerier) inputs(
	projectID uuid.UUID,
	from, to time.Time,
	nameEquals string,
) ([]segments.ResolvedFile, []*segments.SegmentRecord, error) {
	var hints segments.PruningHints
	if nameEquals != "" {
		hints.MetricNames = []string{nameEquals}
	}
	records, err := q.segmentRepo.PlanSegments(projectID, "metrics", from, to, hints)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", records, from, to, nil)
	return tail, records, nil
}

// run returns false when there was nothing to read.
func (q *MetricQuerier) run(ctx context.Context, request query.Request, scan func(*sql.Rows) error) (bool, error) {
	if len(request.Segments) == 0 && len(request.LocalFiles) == 0 && len(request.Slices) == 0 {
		return false, nil
	}
	if request.Stream == "" {
		request.Stream = "metrics"
	}
	err := q.coordinator.Query(ctx, request, scan)
	if errors.Is(err, query.ErrNoData) {
		return false, nil
	}
	return err == nil, err
}

// matcherSQL compiles PromQL matchers to a DuckDB predicate; an absent label
// reads as "", as in Prometheus. Regexes are checked with Go's RE2 first so
// an invalid one fails here with the label name, not inside DuckDB.
func matcherSQL(matchers []metrics_core.MatcherDTO) (string, []any, error) {
	var parts []string
	var args []any
	for _, m := range matchers {
		value := "metric_name"
		if m.Name != "__name__" {
			value = "coalesce(json_extract_string(labels, ?), '')"
			args = append(args, labelPath(m.Name))
		}
		switch m.Type {
		case metrics_core.MatchEqual:
			parts = append(parts, value+" = ?")
		case metrics_core.MatchNotEqual:
			parts = append(parts, value+" <> ?")
		case metrics_core.MatchRegexp, metrics_core.MatchNotRegexp:
			if _, err := regexp.Compile("^(?:" + m.Value + ")$"); err != nil {
				return "", nil, fmt.Errorf("invalid regex matcher for label %q: %w", m.Name, err)
			}
			predicate := "regexp_full_match(" + value + ", ?)"
			if m.Type == metrics_core.MatchNotRegexp {
				predicate = "NOT " + predicate
			}
			parts = append(parts, predicate)
		default:
			return "", nil, fmt.Errorf("unknown matcher type %q", m.Type)
		}
		args = append(args, m.Value)
	}
	return strings.Join(parts, " AND "), args, nil
}

// labelPath is name's JSON path, quoted: label names are normalized to
// [a-zA-Z0-9_], but a quote here must not end the path.
func labelPath(name string) string {
	return `$."` + strings.ReplaceAll(name, `"`, `\"`) + `"`
}

func nameEqualityOf(matchers []metrics_core.MatcherDTO) string {
	for _, m := range matchers {
		if m.Name == "__name__" && m.Type == metrics_core.MatchEqual {
			return m.Value
		}
	}
	return ""
}

func decodeLabels(raw string) map[string]string {
	if raw == "" || raw == "{}" {
		return nil
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	return decoded
}

var noUpperBound = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// minRepresentableTime/maxRepresentableTime sit just inside TIMESTAMP_NS's
// range. Out-of-range input is clamped, not rejected: callers use extreme
// sentinels (e.g. time.Time{}) to mean "unbounded".
var (
	minRepresentableTime = time.Date(1678, 1, 1, 0, 0, 0, 0, time.UTC)
	maxRepresentableTime = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)
)

func resolveTimeRange(timeRange *metrics_core.TimeRangeDTO) (time.Time, time.Time) {
	from := time.Unix(0, 0).UTC()
	to := noUpperBound

	if timeRange != nil {
		if timeRange.From != nil {
			from = *timeRange.From
		}
		if timeRange.To != nil {
			to = *timeRange.To
		}
	}

	return clampToRepresentableRange(from), clampToRepresentableRange(to)
}

func clampToRepresentableRange(t time.Time) time.Time {
	if t.Before(minRepresentableTime) {
		return minRepresentableTime
	}
	if t.After(maxRepresentableTime) {
		return maxRepresentableTime
	}
	return t
}
