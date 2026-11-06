package metrics_querier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	metrics_compactor "obzly/internal/features/metrics/compactor"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	"obzly/internal/query"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// SQL matchers select exactly the series the Go matcher they replaced does
// (kept here as the oracle) - absent labels as "", fully anchored regexes -
// in-process and spread over executors.
func Test_SelectSeries_SQLMatchers_MatchTheGoOracle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	inProcess := newParityQuerier(t, nil)
	spread := newParityQuerier(t, func(q *MetricQuerier) []query.Executor {
		records, err := q.segmentRepo.ListByProject(projectID, "metrics")
		require.NoError(t, err)
		var executors []query.Executor
		for attempt := 0; len(query.Assign(records, executors)) < 2; attempt++ {
			executors = nil
			for i := range 3 {
				executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d-%d", i, attempt), q.db, nil, ""))
			}
		}
		return executors
	})
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}
	for _, matchers := range parityMatcherSets() {
		want := oracleSeries(t, all, matchers)
		for name, q := range map[string]*MetricQuerier{"in-process": inProcess, "spread": spread} {
			response, err := q.SelectSeries(context.Background(), projectID,
				&metrics_core.SelectSeriesRequestDTO{Matchers: matchers, TimeRange: window})
			require.NoError(t, err)
			got := map[string]int{}
			for _, series := range response.Series {
				got[seriesKey(series.MetricName, series.Labels)] = len(series.Samples)
			}
			require.Equal(t, want, got, "%s %+v", name, matchers)
		}
	}
}

// Fragments stop one row past the cap: exactly the cap still answers in
// full, one more is refused, in-process and over executors.
func Test_SelectSeries_RawRowCap_LimitsFragmentsAndRefusesPastIt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	inProcess := newParityQuerier(t, nil)
	spread := newParityQuerier(t, func(q *MetricQuerier) []query.Executor {
		return []query.Executor{
			query.NewLocalExecutor("e0", q.db, nil, ""), query.NewLocalExecutor("e1", q.db, nil, ""),
		}
	})
	original := maxSeriesRows
	t.Cleanup(func() { maxSeriesRows = original })
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	request := &metrics_core.SelectSeriesRequestDTO{
		Matchers:  []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchRegexp, Value: ".+"}},
		TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
	}

	for name, q := range map[string]*MetricQuerier{"in-process": inProcess, "spread": spread} {
		maxSeriesRows = len(all)
		response, err := q.SelectSeries(context.Background(), projectID, request)
		require.NoError(t, err, name)
		samples := 0
		for _, series := range response.Series {
			samples += len(series.Samples)
		}
		require.Equal(t, len(all), samples, name)

		maxSeriesRows = len(all) - 1
		_, err = q.SelectSeries(context.Background(), projectID, request)
		require.ErrorContains(t, err, "more than", name)
	}
}

// Two-phase series select (series stream -> ids -> samples) selects the same
// series and samples as the Go oracle, in-process and over executors.
func Test_SelectSeries_ByID_MatchesTheGoOracle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", from.Add(-time.Minute), projectID).Error)
	inProcess := newParityQuerier(t, nil)
	spread := newParityQuerier(t, func(q *MetricQuerier) []query.Executor {
		records, err := q.segmentRepo.ListByProject(projectID, "metrics")
		require.NoError(t, err)
		var executors []query.Executor
		for attempt := 0; len(query.Assign(records, executors)) < 2; attempt++ {
			executors = nil
			for i := range 3 {
				executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d-%d", i, attempt), q.db, nil, ""))
			}
		}
		return executors
	})
	covered, err := inProcess.seriesCovered(projectID, from)
	require.NoError(t, err)
	require.True(t, covered)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}

	for _, matchers := range parityMatcherSets() {
		want := oracleSeries(t, all, matchers)
		for name, q := range map[string]*MetricQuerier{"in-process": inProcess, "spread": spread} {
			response, err := q.SelectSeries(context.Background(), projectID,
				&metrics_core.SelectSeriesRequestDTO{Matchers: matchers, TimeRange: window})
			require.NoError(t, err)
			got := map[string]int{}
			for _, series := range response.Series {
				got[seriesKey(series.MetricName, series.Labels)] = len(series.Samples)
				require.True(t, slices.IsSortedFunc(series.Samples, func(a, b metrics_core.SampleDTO) int {
					return a.Timestamp.Compare(b.Timestamp)
				}), "samples in time order")
			}
			require.Equal(t, want, got, "%s %+v", name, matchers)
		}
	}
}

func Test_MatcherSQL_InvalidRegex_NamesTheLabel(t *testing.T) {
	_, _, err := matcherSQL([]metrics_core.MatcherDTO{{Name: "job", Type: metrics_core.MatchRegexp, Value: "("}})

	require.ErrorContains(t, err, `label "job"`)
}

// seedParitySeries writes 12 series of 5 samples over four uploaded
// segments: some without a zone label, one with an empty job, paths with
// regex metacharacters.
func seedParitySeries(t *testing.T, now time.Time) (uuid.UUID, []*metrics_core.MetricSample) {
	t.Helper()
	// These segments are marked uploaded: hold the lock the uploader tests
	// take before resetting every uploaded metrics segment.
	test_utils.AcquireExclusiveGlobalQueue(t, "metrics-uploaded-queue")
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	labelSets := []map[string]string{
		{"job": "api", "path": "/v1/users", "code": "200", "zone": "a"},
		{"job": "api", "path": "/v1/users", "code": "500", "zone": "b"},
		{"job": "api", "path": "/v1/orders", "code": "404"},
		{"job": "api", "path": "/v2/orders", "code": "201"},
		{"job": "web", "path": "/a.b", "code": "200", "zone": "a"},
		{"job": "web", "path": "/axb", "code": "503"},
		{"job": "", "path": "/", "code": "200", "zone": ""},
		{"job": "batch"},
	}
	names := []string{"http_requests_total", "http_requests_total", "http_requests_total", "http_requests_total",
		"http_errors", "http_errors", "queue_depth", "queue_depth"}
	var all []*metrics_core.MetricSample
	for i, labels := range labelSets {
		for sample := range 5 {
			all = append(all, &metrics_core.MetricSample{
				ID: uuid.New(), ProjectID: projectID, MetricName: names[i], Labels: labels,
				Value: float64(i*10 + sample), Timestamp: now.Add(-time.Duration(sample*7+i) * time.Minute),
				Type: metrics_core.MetricTypeGauge,
			})
		}
	}
	for segment := range 4 {
		var batch []*metrics_core.MetricSample
		for i, sample := range all {
			if i%4 == segment {
				batch = append(batch, sample)
			}
		}
		require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 4)
	for _, record := range records {
		require.NoError(t, repo.MarkUploaded(record.ID, "metrics/"+record.ID.String()+".parquet"))
	}
	return projectID, all
}

func newParityQuerier(t *testing.T, executors func(*MetricQuerier) []query.Executor) *MetricQuerier {
	t.Helper()
	q, err := NewMetricQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	if executors != nil {
		remote := executors(q)
		q.coordinator, err = query.NewCoordinator(filepath.Join(t.TempDir(), "spread"), q.db,
			query.NewLocalExecutor("local", q.db, nil, ""), func() []query.Executor { return remote },
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, err)
	}
	return q
}

// oracleSeries is each matching series' sample count, by the Go matcher
// SQL replaced.
func oracleSeries(
	t *testing.T,
	samples []*metrics_core.MetricSample,
	matchers []metrics_core.MatcherDTO,
) map[string]int {
	t.Helper()
	result := map[string]int{}
	for _, sample := range samples {
		if oracleMatch(t, matchers, sample.MetricName, sample.Labels) {
			result[seriesKey(sample.MetricName, sample.Labels)]++
		}
	}
	return result
}

func oracleMatch(t *testing.T, matchers []metrics_core.MatcherDTO, metricName string, labels map[string]string) bool {
	for _, m := range matchers {
		actual := metricName
		if m.Name != "__name__" {
			actual = labels[m.Name]
		}
		var ok bool
		switch m.Type {
		case metrics_core.MatchEqual:
			ok = actual == m.Value
		case metrics_core.MatchNotEqual:
			ok = actual != m.Value
		case metrics_core.MatchRegexp, metrics_core.MatchNotRegexp:
			re, err := regexp.Compile("^(?:" + m.Value + ")$")
			require.NoError(t, err)
			ok = re.MatchString(actual) == (m.Type == metrics_core.MatchRegexp)
		}
		if !ok {
			return false
		}
	}
	return true
}

func seriesKey(metricName string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	key := metricName
	for _, k := range keys {
		key += fmt.Sprintf("|%s=%s", k, labels[k])
	}
	return key
}

// Ingest stores each sample's series id.
func Test_Ingest_StoresSeriesIDs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	q := newParityQuerier(t, nil)
	records, err := q.segmentRepo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	files := make([]segments.ResolvedFile, len(records))
	for i, record := range records {
		files[i] = segments.ResolvedFile{Path: record.Path}
	}
	source, args := segments.SourceExpr("metrics", files)

	var total, wrong int
	require.NoError(t, q.db.QueryRow("SELECT count(*), count(*) FILTER (WHERE series_id IS NULL OR "+
		"series_id <> obzly_series_id(metric_name, labels)) FROM "+source, args...).Scan(&total, &wrong))

	require.Equal(t, len(all), total)
	require.Zero(t, wrong)
}

// Ingest writes one metrics_series row per series with its first and last
// sample time, and notes when the project's series stream started.
func Test_Ingest_WritesOneSeriesRowPerSeries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	q := newParityQuerier(t, nil)
	records, err := q.segmentRepo.ListByProject(projectID, seriesStream)
	require.NoError(t, err)
	require.NotEmpty(t, records)
	files := make([]segments.ResolvedFile, len(records))
	for i, record := range records {
		files[i] = segments.ResolvedFile{Path: record.Path}
	}
	source, args := segments.SourceExpr(seriesStream, files)

	rows, err := q.db.Query("SELECT series_id, metric_name, labels, min(first_seen), max(last_seen) FROM "+source+
		" GROUP BY ALL", args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type span struct{ first, last time.Time }
	want := map[uint64]span{}
	for _, sample := range all {
		id := segments.SeriesID(sample.MetricName, labelsJSON(t, sample.Labels))
		current, ok := want[id]
		if !ok || sample.Timestamp.Before(current.first) {
			current.first = sample.Timestamp
		}
		if !ok || sample.Timestamp.After(current.last) {
			current.last = sample.Timestamp
		}
		want[id] = current
	}
	got := map[uint64]span{}
	for rows.Next() {
		var id uint64
		var name, labels string
		var first, last time.Time
		require.NoError(t, rows.Scan(&id, &name, &labels, &first, &last))
		require.Equal(t, segments.SeriesID(name, labels), id)
		got[id] = span{first.UTC(), last.UTC()}
	}
	require.Equal(t, want, got)
	covered, err := q.seriesCovered(projectID, now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, covered, "coverage recorded at the first series flush")
}

// Label lookups answer the same from the series stream as from samples.
func Test_LabelLookups_SeriesStream_MatchSamples(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, _ := seedParitySeries(t, now)
	q := newParityQuerier(t, nil)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}
	ask := func() (*metrics_core.ListLabelNamesResponseDTO, *metrics_core.ListLabelValuesResponseDTO,
		*metrics_core.ListMetricNamesResponseDTO) {
		matchers := []metrics_core.MatcherDTO{{Name: "job", Type: metrics_core.MatchRegexp, Value: "api|web"}}
		names, err := q.ListLabelNames(context.Background(), projectID,
			&metrics_core.ListLabelNamesRequestDTO{Matchers: matchers, TimeRange: window})
		require.NoError(t, err)
		values, err := q.ListLabelValues(context.Background(), projectID,
			&metrics_core.ListLabelValuesRequestDTO{LabelName: "path", Matchers: matchers, TimeRange: window})
		require.NoError(t, err)
		metricNames, err := q.ListMetricNames(context.Background(), projectID, window)
		require.NoError(t, err)
		return names, values, metricNames
	}

	covered, err := q.seriesCovered(projectID, from)
	require.NoError(t, err)
	require.False(t, covered, "the window starts before coverage: samples")
	wantNames, wantValues, wantMetrics := ask()
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", from.Add(-time.Minute), projectID).Error)
	covered, err = q.seriesCovered(projectID, from)
	require.NoError(t, err)
	require.True(t, covered)
	gotNames, gotValues, gotMetrics := ask()

	require.Equal(t, wantNames, gotNames)
	require.Equal(t, wantValues, gotValues)
	require.Equal(t, wantMetrics, gotMetrics)
	require.Contains(t, gotValues.Values, "/a.b")
}

// A label lookup without a start (a dashboard filter's "all time" values)
// is answered from the label index and series stream whole - not by
// scanning every segment's samples for what predates them.
func Test_LabelLookups_WithoutAStart_ReadTheWholeIndex(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, _ := seedParitySeries(t, now)
	q := newParityQuerier(t, nil)
	// Coverage starting after every sample: a windowed lookup needs them.
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", now.Add(time.Hour), projectID).Error)
	require.NoError(t, storage.GetDb().Exec(
		"DELETE FROM stream_segments WHERE project_id = ? AND stream = 'metrics'", projectID).Error)
	from := now.Add(-time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from}
	regex := []metrics_core.MatcherDTO{{Name: "job", Type: metrics_core.MatchRegexp, Value: "api|web"}}

	values, err := q.ListLabelValues(context.Background(), projectID,
		&metrics_core.ListLabelValuesRequestDTO{LabelName: "path"})
	require.NoError(t, err)
	viaStream, err := q.ListLabelValues(context.Background(), projectID,
		&metrics_core.ListLabelValuesRequestDTO{LabelName: "path", Matchers: regex})
	require.NoError(t, err)
	names, err := q.ListLabelNames(context.Background(), projectID, &metrics_core.ListLabelNamesRequestDTO{})
	require.NoError(t, err)
	windowed, err := q.ListLabelValues(context.Background(), projectID,
		&metrics_core.ListLabelValuesRequestDTO{LabelName: "path", TimeRange: window})
	require.NoError(t, err)

	require.Contains(t, values.Values, "/v1/users", "from the index, with no samples left to scan")
	require.Contains(t, viaStream.Values, "/a.b", "from the series stream")
	require.Contains(t, names.Names, "zone")
	require.Empty(t, windowed.Values, "a lookup with a start still reads the samples the index doesn't cover")
}

func labelsJSON(t *testing.T, labels map[string]string) string {
	t.Helper()
	if len(labels) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(labels)
	require.NoError(t, err)
	return string(encoded)
}

// parityMatcherSets: equality, inequality and regexes on names and labels,
// absent labels, anchoring, metacharacters, combinations, no matcher.
func parityMatcherSets() [][]metrics_core.MatcherDTO {
	m := func(name string, matchType metrics_core.MatchType, value string) metrics_core.MatcherDTO {
		return metrics_core.MatcherDTO{Name: name, Type: matchType, Value: value}
	}

	return [][]metrics_core.MatcherDTO{
		{m("__name__", metrics_core.MatchEqual, "http_requests_total")},
		{m("__name__", metrics_core.MatchRegexp, "http_.*")},
		{m("__name__", metrics_core.MatchRegexp, "http")}, // anchored: no partial match
		{m("job", metrics_core.MatchEqual, "api")},
		{m("job", metrics_core.MatchNotEqual, "api")},
		{m("zone", metrics_core.MatchEqual, "")}, // absent label
		{m("zone", metrics_core.MatchNotEqual, "")},
		{m("zone", metrics_core.MatchRegexp, ".*")},
		{m("zone", metrics_core.MatchRegexp, ".+")},
		{m("path", metrics_core.MatchRegexp, `/v1/(users|orders)`)},
		{m("path", metrics_core.MatchNotRegexp, `/v1/.*`)},
		{m("path", metrics_core.MatchEqual, `/a.b`)}, // regex metacharacters in an equality
		{m("code", metrics_core.MatchRegexp, `5..|4\d\d`)},
		{m("__name__", metrics_core.MatchEqual, "http_requests_total"), m("job", metrics_core.MatchEqual, "api"),
			m("code", metrics_core.MatchNotRegexp, "2..")},
		{m("missing", metrics_core.MatchEqual, "x")},
		{},
	}
}

// Label names, values and metric names answer the same from the label index
// as from samples, and the index is what answers them once covered.
func Test_LabelLookups_Index_MatchSamples(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, _ := seedParitySeries(t, now)
	q := newParityQuerier(t, nil)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}
	byName := []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: "http_requests_total"}}
	type answers struct {
		names, namesForMetric, jobs, metricNameValues []string
		metrics                                       []metrics_core.MetricNameInfo
	}
	ask := func() answers {
		var a answers
		names, err := q.ListLabelNames(
			context.Background(),
			projectID,
			&metrics_core.ListLabelNamesRequestDTO{TimeRange: window},
		)
		require.NoError(t, err)
		a.names = names.Names
		names, err = q.ListLabelNames(context.Background(), projectID,
			&metrics_core.ListLabelNamesRequestDTO{Matchers: byName, TimeRange: window})
		require.NoError(t, err)
		a.namesForMetric = names.Names
		values, err := q.ListLabelValues(context.Background(), projectID,
			&metrics_core.ListLabelValuesRequestDTO{LabelName: "job", Matchers: byName, TimeRange: window})
		require.NoError(t, err)
		a.jobs = values.Values
		values, err = q.ListLabelValues(context.Background(), projectID,
			&metrics_core.ListLabelValuesRequestDTO{LabelName: "__name__", TimeRange: window})
		require.NoError(t, err)
		a.metricNameValues = values.Values
		metrics, err := q.ListMetricNames(context.Background(), projectID, window)
		require.NoError(t, err)
		a.metrics = metrics.Metrics
		return a
	}
	want := ask()

	require.NoError(t, storage.GetDb().Exec(
		"UPDATE metric_series_coverage SET since = ? WHERE project_id = ?", from.Add(-time.Minute), projectID).Error)
	got := ask()
	require.Equal(t, want, got)
	require.Equal(t, []string{"api"}, got.jobs, "job values of http_requests_total only")

	// Proof the index answered: empty it and the answers go empty too.
	require.NoError(t, storage.GetDb().Exec("DELETE FROM metric_label_index WHERE project_id = ?", projectID).Error)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM metric_name_index WHERE project_id = ?", projectID).Error)
	emptied := ask()
	require.Equal(t, []string{"__name__"}, emptied.names)
	require.Empty(t, emptied.metrics)
}

// The ingester writes the label index: every label value per metric, and
// each metric's type.
func Test_Ingest_WritesTheLabelIndex(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	want := map[string]bool{}
	for _, sample := range all {
		for name, value := range sample.Labels {
			want[sample.MetricName+"|"+name+"|"+value] = true
		}
	}
	var rows []struct{ MetricName, LabelName, LabelValue string }
	require.NoError(t, storage.GetDb().Raw(
		"SELECT metric_name, label_name, label_value FROM metric_label_index WHERE project_id = ?", projectID).
		Scan(&rows).Error)
	got := map[string]bool{}
	for _, row := range rows {
		got[row.MetricName+"|"+row.LabelName+"|"+row.LabelValue] = true
	}
	require.Equal(t, want, got)

	var types []struct{ MetricName, MetricType string }
	require.NoError(t, storage.GetDb().Raw(
		"SELECT metric_name, metric_type FROM metric_name_index WHERE project_id = ? ORDER BY metric_name", projectID).
		Scan(&types).Error)
	require.Len(t, types, 3)
	require.Equal(t, "gauge", types[0].MetricType)
}

// Label values with quotes, backslashes, commas and braces survive the
// index's array-literal upsert unchanged.
func Test_LabelIndex_KeepsAwkwardValues(t *testing.T) {
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	awkward := `say "hi", \path\ {x} ` + "tab\t"
	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {{
		ID: uuid.New(), ProjectID: projectID, MetricName: "odd", Labels: map[string]string{"v": awkward},
		Value: 1, Timestamp: time.Now().UTC(), Type: metrics_core.MetricTypeGauge,
	}}}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)

	var values []string
	require.NoError(t, storage.GetDb().Raw(
		"SELECT label_value FROM metric_label_index WHERE project_id = ?", projectID).Scan(&values).Error)
	require.Equal(t, []string{awkward}, values)
}

// Rollups: the pass builds them once per cold segment;
// a coarse-step select then reads them, returning real samples only (the
// bucket extremes) with each series' max unchanged; a fine step reads raw.
// (How much a rollup thins the samples is BuildRollup's test.)
func Test_SelectSeries_CoarseStep_ReadsRollupsOfColdSegments(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID, all := seedParitySeries(t, now)
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	require.NoError(t, repo.SetLevel(ids, segments.LevelCold))
	compactor, err := metrics_compactor.NewMetricCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	// One cold segment a pass, then nothing left to do.
	for range len(records) + 1 {
		require.NoError(t, compactor.RollupColdSegments(projectID))
	}
	for stream := range segments.RollupStreams {
		rollups, err := repo.ListByProject(projectID, stream)
		require.NoError(t, err)
		require.Len(t, rollups, len(records), stream)
	}

	q := newParityQuerier(t, nil)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}
	selectWith := func(stepMs, rangeMs int64) map[string][]metrics_core.SampleDTO {
		response, err := q.SelectSeries(context.Background(), projectID,
			&metrics_core.SelectSeriesRequestDTO{TimeRange: window, StepMs: stepMs, RangeMs: rangeMs})
		require.NoError(t, err)
		result := map[string][]metrics_core.SampleDTO{}
		for _, series := range response.Series {
			result[seriesKey(series.MetricName, series.Labels)] = series.Samples
		}
		return result
	}
	selectAt := func(stepMs int64) map[string][]metrics_core.SampleDTO { return selectWith(stepMs, 0) }
	raw := selectAt(0)
	require.Equal(t, raw, selectAt(60_000), "below 5 minutes: raw")
	require.Len(t, raw, 8)
	rawPoints := map[string]float64{}
	for key, samples := range raw {
		for _, sample := range samples {
			rawPoints[fmt.Sprintf("%s@%d", key, sample.Timestamp.UnixNano())] = sample.Value
		}
	}
	// Proof the rollups are what answer: without the sample files a raw
	// read finds nothing (a missing file is skipped).
	for _, record := range records {
		require.NoError(t, os.Remove(record.Path))
	}
	require.Empty(t, selectAt(0))
	require.Empty(t, selectWith(3_600_000, 10*60_000), "rate(x[10m]): too few buckets in the window, raw")
	require.NotEmpty(t, selectWith(3_600_000, 3_600_000), "rate(x[1h]): 5m buckets")

	for _, stepMs := range []int64{300_000, 3_600_000} {
		rolled := selectAt(stepMs)
		require.Len(t, rolled, len(raw))
		for key, samples := range rolled {
			maxRaw, maxRolled := math.Inf(-1), math.Inf(-1)
			for _, sample := range raw[key] {
				maxRaw = math.Max(maxRaw, sample.Value)
			}
			for _, sample := range samples {
				value, ok := rawPoints[fmt.Sprintf("%s@%d", key, sample.Timestamp.UnixNano())]
				require.True(t, ok, "%s: a rolled-up point is a real sample", key)
				require.Equal(t, value, sample.Value)
				maxRolled = math.Max(maxRolled, sample.Value)
			}
			require.Equal(t, maxRaw, maxRolled, "%s: the max survives", key)
		}
	}
	require.NotEmpty(t, all)
}

func Test_RollupFor_NeedsBucketsInTheRangeWindow(t *testing.T) {
	const minute = int64(60_000)
	for _, c := range []struct {
		step, window int64
		want         string
	}{
		{minute, 0, ""},
		{5 * minute, 0, "metrics_5m"},
		{60 * minute, 0, "metrics_1h"},
		{60 * minute, 60 * minute, "metrics_5m"},
		{60 * minute, 5 * 60 * minute, "metrics_1h"},
		{5 * minute, 20 * minute, ""},
		{5 * minute, 25 * minute, "metrics_5m"},
	} {
		require.Equal(t, c.want, rollupFor(c.step, c.window), "step %dm window %dm", c.step/minute, c.window/minute)
	}
}
