package webanalytics

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

const (
	// scanMaxRows caps one statement's rows; the address-prefix list is the
	// largest (one row per /24 or /48 per source and host).
	scanMaxRows = 200_000
	// prefixLimit: address prefixes looked up per source and host and window.
	prefixLimit = 20_000
	// uaLimit: distinct User-Agents parsed per source and host and window.
	uaLimit = 2_000
)

// scanRequest is one read of raw logs: counts per bucket over [from, to),
// top lists over [keysFrom, to).
type scanRequest struct {
	projectID uuid.UUID
	from, to  time.Time
	keysFrom  time.Time
	bucket    time.Duration
	options   scanOptions
	// timeout bounds one slice's statements.
	timeout time.Duration
	// slice is the longest range one read covers (0: the whole range).
	slice time.Duration
	// deadline: no slice starts after it and each ends by it (zero: none).
	deadline time.Time
	// partial leaves out a slice that fails or doesn't fit before the
	// deadline instead of failing the scan (see runScan).
	partial bool
}

// minScanSlice: a slice that runs out of memory or spill is split in
// halves while the halves are at least this long.
const minScanSlice = 5 * time.Minute

// scanError is a failed scan, with what it got through.
type scanError struct {
	projectID uuid.UUID
	// from, to: the slice that failed.
	from, to time.Time
	// rowsRead: access lines in the slices that finished.
	rowsRead int64
	slices   int
	err      error
}

func (e *scanError) Error() string {
	return fmt.Sprintf("web analytics scan of project %s failed for %s to %s after %d slices (%d rows read): %v",
		e.projectID, e.from.Format(time.RFC3339), e.to.Format(time.RFC3339), e.slices, e.rowsRead, e.err)
}

func (e *scanError) Unwrap() error { return e.err }

// timeWindow is one slice of a scan.
type timeWindow struct{ from, to time.Time }

// keyStatement is one top list's query.
type keyStatement struct {
	dim     int
	keyExpr string
	filter  string
	limit   int
	extra   string
	sketch  bool
}

// keyStatements: every list a scan reads. hidden is a crawler when the
// settings leave crawlers out of top lists; request-level shares (status,
// method, protocol, TLS) keep them, as Requests does.
var keyStatements = []keyStatement{
	{dim: dimPage, keyExpr: "page", filter: "NOT hidden", limit: 100, sketch: true},
	{dim: dimStatic, keyExpr: "path", filter: "is_static AND NOT hidden", limit: 100},
	{dim: dimNotFound, keyExpr: "path", filter: "status = 404 AND NOT hidden", limit: 100, sketch: true},
	{dim: dimReferrer, keyExpr: "ref_host", filter: "ref_host IS DISTINCT FROM host AND NOT hidden", limit: 100,
		sketch: true},
	{dim: dimIP, keyExpr: "ipkey", filter: "NOT hidden", limit: 100, extra: "any_value(prefix)"},
	{dim: dimMethod, keyExpr: "method", filter: "true", limit: 20},
	{dim: dimProtocol, keyExpr: "proto", filter: "true", limit: 20},
	{dim: dimTLS, keyExpr: "tls", filter: "true", limit: 20},
	{dim: dimStatus, keyExpr: "CAST(status AS VARCHAR)", filter: "true", limit: 60},
	{dim: dimUA, keyExpr: "ua", filter: "true", limit: uaLimit, extra: "CAST(bool_or(bot) AS VARCHAR)", sketch: true},
	{dim: dimPrefix, keyExpr: "prefix", filter: "NOT hidden", limit: prefixLimit, sketch: true},
}

// runScan reads [from, to) of a project's raw logs into a rollup (not yet
// finished: see rollup.finish), in slices of request.slice, newest first,
// merged. A slice that runs out of memory or spill is split in halves (down
// to minScanSlice), so a busy hour still fits the handle's caps. Counts and
// visitors merge exactly; a top list merges each slice's top rows, so a key
// just below the cut in some slices counts fewer hits (as hours merge into
// days). With request.partial, a slice that still fails, or doesn't start
// before the deadline, is left out: the rollup is marked partial and comes
// back with a *scanError for the first failure.
func runScan(repository logs_core.LogRepository, request scanRequest) (*rollup, error) {
	merged := newRollup()
	pending := sliceWindows(request.from, request.to, request.slice)
	slices := 0
	var firstErr error
	fail := func(window timeWindow, err error) error {
		scanErr := &scanError{projectID: request.projectID, from: window.from, to: window.to,
			rowsRead: merged.rowsRead, slices: slices, err: err}
		if firstErr == nil {
			firstErr = scanErr
		}
		return scanErr
	}
	for len(pending) > 0 {
		window := pending[0]
		pending = pending[1:]
		sliceRequest := request
		sliceRequest.from, sliceRequest.to = window.from, window.to
		if !request.deadline.IsZero() && !time.Now().Before(request.deadline) {
			err := fail(window, errors.New("out of time before this slice started"))
			if !request.partial {
				return nil, err
			}
			merged.partial = true
			continue
		}
		part, err := scanSlice(repository, sliceRequest)
		if err != nil {
			if half := window.to.Sub(window.from) / 2; outOfMemory(err) && half >= minScanSlice {
				middle := window.from.Add(half).Truncate(time.Minute)
				if middle.After(window.from) {
					pending = append([]timeWindow{{middle, window.to}, {window.from, middle}}, pending...)
					continue
				}
			}
			scanErr := fail(window, err)
			if !request.partial {
				return nil, scanErr
			}
			merged.partial = true
			continue
		}
		merged.merge(part)
		slices++
	}
	return merged, firstErr
}

// sliceWindows splits [from, to) at multiples of slice (all of it when slice
// is 0 or longer), newest first.
func sliceWindows(from, to time.Time, slice time.Duration) []timeWindow {
	if slice <= 0 || to.Sub(from) <= slice {
		return []timeWindow{{from, to}}
	}
	var windows []timeWindow
	for end := to; end.After(from); {
		start := end.Add(-time.Nanosecond).Truncate(slice)
		if start.Before(from) {
			start = from
		}
		windows = append(windows, timeWindow{start, end})
		end = start
	}
	return windows
}

// outOfMemory: DuckDB ran out of its memory limit or spill cap.
func outOfMemory(err error) bool {
	text := err.Error()
	return strings.Contains(text, "Out of Memory") || strings.Contains(text, "max_temp_directory_size")
}

// scanSlice reads one slice: its access lines (rawFilterSQL narrows the
// segment scan, then tableSQL shapes them) and every statement over them.
func scanSlice(repository logs_core.LogRepository, request scanRequest) (*rollup, error) {
	table, tableArgs := tableSQL(request.options)
	interval := fmt.Sprintf("INTERVAL '%d seconds'", int64(request.bucket/time.Second))
	statements := []logs_core.ScopedSQLStatement{
		{SQL: totalsSQL(interval)},
		{SQL: visitorsSQL(interval)},
		{SQL: histogramSQL(interval)},
	}
	for _, statement := range keyStatements {
		sql, args := statement.sql(request.keysFrom)
		statements = append(statements, logs_core.ScopedSQLStatement{SQL: sql, Args: args})
	}
	results, err := repository.ExecuteScopedSQL(request.projectID, &logs_core.ScopedSQLRequestDTO{
		From:        request.from,
		To:          request.to.Add(-time.Nanosecond),
		Statements:  statements,
		Table:       table,
		TableArgs:   tableArgs,
		TableFilter: accessFilterSQL,
		RawColumns:  rawColumns,
		RawFilter:   rawFilterSQL(),
		Timeout:     request.timeout,
		MaxRows:     scanMaxRows,
		Deadline:    request.deadline,
	})
	if err != nil {
		return nil, err
	}
	if len(results) != len(statements) {
		return nil, fmt.Errorf(
			"web analytics scan returned %d results for %d statements",
			len(results),
			len(statements),
		)
	}
	result := newRollup()
	parseTotals(result, results[0].Rows)
	parseVisitors(result, results[1].Rows)
	parseHistogram(result, results[2].Rows)
	for i, statement := range keyStatements {
		parseKeys(result, statement, results[3+i].Rows)
	}
	return result, nil
}

func totalsSQL(interval string) string {
	columns := []string{
		"time_bucket(" + interval + ", ts) AS b", "src", "host", "count(*)",
		"CAST(coalesce(sum(bytes), 0) AS BIGINT)",
		"count(*) FILTER (WHERE status BETWEEN 200 AND 299)",
		"count(*) FILTER (WHERE status BETWEEN 300 AND 399)",
		"count(*) FILTER (WHERE status BETWEEN 400 AND 499)",
		"count(*) FILTER (WHERE status >= 500)",
		"count(*) FILTER (WHERE status < 200)",
		"count(*) FILTER (WHERE status = 404)",
		"count(*) FILTER (WHERE bot)",
		"count(client)", "count(bytes)", "count(ref_host)", "count(ua)", "count(dur)", "count(tls)",
		"count(NULLIF(host, ''))", "count(proto)",
		"any_value(id_service)", "any_value(id_hostname)", "any_value(id_cluster)", "any_value(id_namespace)",
	}
	for _, field := range accessFields {
		columns = append(columns, "mode(ai_"+shortName(field.name)+")")
	}
	return "SELECT " + strings.Join(columns, ", ") + " FROM logs GROUP BY 1, 2, 3"
}

func visitorsSQL(interval string) string {
	return "SELECT b, src, host, " + registersAggSQL() + " FROM (SELECT time_bucket(" + interval +
		", ts) AS b, src, host, " + registerSQL(totalPrecision) + " FROM logs WHERE vhash IS NOT NULL GROUP BY 1, 2, 3, 4) " +
		"GROUP BY 1, 2, 3"
}

func histogramSQL(interval string) string {
	var whens []string
	for i, bound := range durationBoundsMs {
		whens = append(whens, fmt.Sprintf("WHEN dur < %g THEN %d", bound, i))
	}
	return "SELECT time_bucket(" + interval + ", ts) AS b, src, host, CASE " + strings.Join(whens, " ") +
		fmt.Sprintf(" ELSE %d END AS bi, count(*) FROM logs WHERE dur IS NOT NULL AND dur >= 0 GROUP BY 1, 2, 3, 4",
			len(durationBoundsMs))
}

// sql is the statement for one list: its top rows per source and host,
// with a visitors sketch per row for the lists that show visitors.
func (k keyStatement) sql(keysFrom time.Time) (string, []any) {
	extra := "''"
	if k.extra != "" {
		extra = "coalesce(" + k.extra + ", '')"
	}
	where := "ts >= ? AND (" + k.filter + ") AND (" + k.keyExpr + ") IS NOT NULL"
	ranked := "SELECT src, host, " + k.keyExpr + " AS k, count(*) AS hits, " +
		"CAST(coalesce(sum(bytes), 0) AS BIGINT) AS bytes, count(*) FILTER (WHERE status >= 500) AS e5, " +
		"count(*) FILTER (WHERE ref_host IS NOT NULL AND ref_host <> '" + directReferrer + "') AS referred, " +
		"max(ts) AS last_seen, " + extra + " AS extra FROM logs WHERE " + where + " GROUP BY 1, 2, 3"
	top := "SELECT * FROM (SELECT *, row_number() OVER (PARTITION BY src, host ORDER BY hits DESC, k) AS rn FROM (" +
		ranked + ") agg) ranked WHERE rn <= " + strconv.Itoa(k.limit)
	if !k.sketch {
		return "SELECT src, host, k, hits, bytes, e5, referred, last_seen, extra, NULL FROM (" + top + ") top",
			[]any{keysFrom}
	}
	registers := "SELECT lk.src, lk.host, lk.k, " + registerSQLOn(
		"lk.vhash",
		keyPrecision,
	) + " FROM (SELECT src, host, " +
		k.keyExpr + " AS k, vhash FROM logs WHERE " + where + " AND vhash IS NOT NULL) lk " +
		"JOIN top ON lk.src = top.src AND lk.host = top.host AND lk.k = top.k GROUP BY 1, 2, 3, 4"
	return "WITH top AS (" + top + "), sk AS (SELECT src, host, k, " + registersAggSQL() + " FROM (" + registers +
		") regs GROUP BY 1, 2, 3) SELECT top.src, top.host, top.k, hits, bytes, e5, referred, last_seen, extra, sk.s " +
		"FROM top LEFT JOIN sk ON top.src = sk.src AND top.host = sk.host AND top.k = sk.k", []any{keysFrom, keysFrom}
}

// registerSQL is a HyperLogLog register and its smallest remainder.
func registerSQL(p int) string {
	return registerSQLOn("vhash", p)
}

func registerSQLOn(column string, p int) string {
	return fmt.Sprintf("(%s & %d) AS r, min(%s >> %d) AS w", column, (1<<p)-1, column, p)
}

func registersAggSQL() string {
	return "string_agg(CAST(r AS VARCHAR) || ':' || CAST(w AS VARCHAR), ',') AS s"
}

func parseTotals(result *rollup, rows [][]any) {
	for _, row := range rows {
		if len(row) < 24+len(accessFields) {
			continue
		}
		key := bucketKey{toTime(row[0]), toString(row[1]), toString(row[2])}
		totals := newTotals()
		totals.requests = toInt64(row[3])
		totals.bytes = toInt64(row[4])
		totals.s2, totals.s3, totals.s4, totals.s5 = toInt64(row[5]), toInt64(row[6]), toInt64(row[7]), toInt64(row[8])
		totals.sOther = toInt64(row[9])
		totals.notFound = toInt64(row[10])
		totals.bots = toInt64(row[11])
		for i := range withCount {
			totals.with[i] = toInt64(row[12+i])
		}
		totals.service, totals.hostName = toString(row[20]), toString(row[21])
		totals.cluster, totals.namespace = toString(row[22]), toString(row[23])
		aliases := map[string]int{}
		for i, field := range accessFields {
			// An alias index from DuckDB; anything outside a small positive range is ignored.
			if index := toInt64(row[24+i]); index > 0 && index <= math.MaxInt32 {
				aliases[field.name] = int(index)
			}
		}
		totals.format, totals.formatWeight = encodeFormat(aliases), totals.requests
		result.rowsRead += totals.requests
		if existing := result.totals[key]; existing != nil {
			existing.merge(totals)
		} else {
			result.totals[key] = totals
		}
	}
}

func parseVisitors(result *rollup, rows [][]any) {
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		totals := result.totals[bucketKey{toTime(row[0]), toString(row[1]), toString(row[2])}]
		if totals != nil {
			totals.visitors.addRegisters(toString(row[3]))
		}
	}
}

func parseHistogram(result *rollup, rows [][]any) {
	for _, row := range rows {
		if len(row) < 5 {
			continue
		}
		totals := result.totals[bucketKey{toTime(row[0]), toString(row[1]), toString(row[2])}]
		index := toInt64(row[3])
		if totals != nil && index >= 0 && index <= math.MaxInt16 && index < int64(len(totals.hist)) {
			totals.hist[int(index)] += toInt64(row[4])
		}
	}
}

func parseKeys(result *rollup, statement keyStatement, rows [][]any) {
	for _, row := range rows {
		if len(row) < 10 {
			continue
		}
		sh := srcHost{toString(row[0]), toString(row[1])}
		agg := &keyAgg{
			hits: toInt64(row[3]), bytes: toInt64(row[4]), e5: toInt64(row[5]), referred: toInt64(row[6]),
			lastSeen: toTime(row[7]), extra: toString(row[8]),
		}
		if statement.sketch {
			agg.visitors = newSketch(keyPrecision)
			agg.visitors.addRegisters(toString(row[9]))
		}
		set := result.keys[sh]
		if set == nil {
			set = keySet{}
			result.keys[sh] = set
		}
		set.add(statement.dim, toString(row[2]), agg)
	}
}

func toInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int16:
		return int64(typed)
	case int8:
		return int64(typed)
	case int:
		return int64(typed)
	case uint64:
		return int64(typed)
	case uint32:
		return int64(typed)
	case float64:
		return int64(typed)
	case *big.Int:
		return typed.Int64()
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func toString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func toTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
