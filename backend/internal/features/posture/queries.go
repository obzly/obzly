package posture

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/posture/baseline"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

const (
	dayLayout    = "2006-01-02"
	trendDays    = 30
	maxDailySpan = 92
	maxRangeDays = 1900
)

// StandardScore is a standard's passed controls of those evaluated. Controls failed only by
// suppressed findings count as passed; controls nothing evaluated aren't counted at all.
type StandardScore struct {
	Standard  string       `json:"standard"`
	Name      string       `json:"name"`
	Passed    int          `json:"passed"`
	Failed    int          `json:"failed"`
	Evaluated int          `json:"evaluated"`
	Percent   float64      `json:"percent"`
	Sources   []string     `json:"sources"`
	Trend     []ScorePoint `json:"trend,omitempty"`
}

type ScorePoint struct {
	Day     string  `json:"day"`
	Percent float64 `json:"percent"`
}

type CoverageCounts struct {
	Native     int `json:"native"`
	Fallback   int `json:"fallback"`
	Baseline   int `json:"baseline"`
	NotCovered int `json:"notCovered"`
	Stale      int `json:"stale"`
}

type SummaryDTO struct {
	Standards []StandardScore `json:"standards"`
	// Open: open findings that aren't suppressed, by severity.
	Open       map[string]int `json:"open"`
	Suppressed int            `json:"suppressed"`
	// Resolved30d: findings resolved in the last 30 days.
	Resolved30d int            `json:"resolved30d"`
	Coverage    CoverageCounts `json:"coverage"`
	LastScanAt  *time.Time     `json:"lastScanAt,omitempty"`
}

type FindingsRequest struct {
	Filters Filters `json:"filters"`
	// Sort: severity (default), firstSeen, lastSeen or title; Desc reverses it.
	Sort   string `json:"sort,omitempty"`
	Desc   bool   `json:"desc,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type FindingsResponse struct {
	Total    int64     `json:"total"`
	Findings []Finding `json:"findings"`
}

type FacetValue struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

type FacetsResponse struct {
	Dimensions map[string][]FacetValue `json:"dimensions"`
}

// FindingDetail is a finding with its history and, for a baseline check, how to fix it.
type FindingDetail struct {
	Finding
	Events        []Event               `json:"events"`
	StandardNames map[string]string     `json:"standardNames"`
	Remediation   *baseline.Remediation `json:"remediation,omitempty"`
}

// CoverageRow is one scope (connection, account, region): how it's covered, and why.
type CoverageRow struct {
	ConnectionID   uuid.UUID     `json:"connectionId"`
	ConnectionName string        `json:"connectionName"`
	Provider       string        `json:"provider"`
	Account        string        `json:"account"`
	Region         string        `json:"region"`
	Status         string        `json:"status"`
	Reason         string        `json:"reason,omitempty"`
	ScannedAt      time.Time     `json:"scannedAt"`
	Runs           []CoverageRun `json:"runs"`
}

type CoverageRun struct {
	Standard string `json:"standard"`
	Source   string `json:"source"`
	Result   string `json:"result"`
	Reason   string `json:"reason,omitempty"`
	Passed   int    `json:"passed"`
	Failed   int    `json:"failed"`
}

type TrendsRequest struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Granularity string  `json:"granularity"`
	Filters     Filters `json:"filters"`
}

type TrendPeriod struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
	// Open: findings open at the period's end, by severity.
	Open     map[string]int `json:"open"`
	Opened   int            `json:"opened"`
	Resolved int            `json:"resolved"`
	// Scores: each standard's percent at the period's end; absent before it was evaluated.
	Scores map[string]float64 `json:"scores"`
}

type TrendsResponse struct {
	Periods []TrendPeriod `json:"periods"`
}

type period struct {
	Key      string
	From, To time.Time
}

// Summary is the Posture tab's overview.
func (s *Service) Summary(ctx context.Context, projects []uuid.UUID) (*SummaryDTO, error) {
	db := storage.GetDb().WithContext(ctx)
	scores, err := standardScores(db, projects, nil)
	if err != nil {
		return nil, err
	}
	if err := attachTrends(db, projects, scores, s.now()); err != nil {
		return nil, err
	}
	summary := &SummaryDTO{Standards: scores, Open: map[string]int{}}
	var counts []struct {
		Severity   string
		Suppressed bool
		Count      int
	}
	if err := db.Raw(`SELECT severity, suppressed, count(*) AS count FROM posture_findings
		WHERE project_id IN ? AND status = 'open' GROUP BY 1, 2`, projects).Scan(&counts).Error; err != nil {
		return nil, err
	}
	for _, count := range counts {
		if count.Suppressed {
			summary.Suppressed += count.Count
		} else {
			summary.Open[count.Severity] += count.Count
		}
	}
	if err := db.Raw(`SELECT count(*) FROM posture_findings WHERE project_id IN ? AND status = 'resolved'
		AND resolved_at > ?`, projects, s.now().AddDate(0, 0, -30)).Scan(&summary.Resolved30d).Error; err != nil {
		return nil, err
	}
	rows, err := s.Coverage(ctx, projects)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		switch row.Status {
		case CoverageNative:
			summary.Coverage.Native++
		case CoverageFallback:
			summary.Coverage.Fallback++
		case CoverageBaseline:
			summary.Coverage.Baseline++
		case CoverageStale:
			summary.Coverage.Stale++
		default:
			summary.Coverage.NotCovered++
		}
		if summary.LastScanAt == nil || row.ScannedAt.After(*summary.LastScanAt) {
			scanned := row.ScannedAt
			summary.LastScanAt = &scanned
		}
	}
	return summary, nil
}

// Findings lists findings matching the filters, a page at a time.
func (s *Service) Findings(
	ctx context.Context,
	projects []uuid.UUID,
	request FindingsRequest,
) (*FindingsResponse, error) {
	where, args := filterSQL(projects, request.Filters, "")
	limit := min(max(request.Limit, 1), maxPageSize)
	if request.Limit == 0 {
		limit = 50
	}
	order := map[string]string{
		"firstSeen": "first_seen", "lastSeen": "last_seen", "title": "lower(title)", "severity": severityOrder,
	}[request.Sort]
	if order == "" {
		order = severityOrder
	}
	direction := " ASC"
	if request.Desc != (request.Sort == "firstSeen" || request.Sort == "lastSeen") {
		direction = " DESC"
	}
	db := storage.GetDb().WithContext(ctx)
	response := &FindingsResponse{Findings: []Finding{}}
	if err := db.Model(&Finding{}).Where(where, args...).Count(&response.Total).Error; err != nil {
		return nil, err
	}
	err := db.Where(where, args...).Order(order + direction + ", last_seen DESC, id").
		Offset(max(request.Offset, 0)).Limit(limit).Find(&response.Findings).Error
	return response, err
}

// Facets: the values each filter can take with the others applied, with how many findings each.
func (s *Service) Facets(ctx context.Context, projects []uuid.UUID, filters Filters) (*FacetsResponse, error) {
	db := storage.GetDb().WithContext(ctx)
	response := &FacetsResponse{Dimensions: map[string][]FacetValue{}}
	columns := map[string]string{
		"severity": "severity", "provider": "provider", "account": "account", "region": "region",
		"source": "source", "connection": "connection_id::text",
	}
	for dimension, column := range columns {
		where, args := filterSQL(projects, filters, dimension)
		var values []FacetValue
		label := "''"
		if dimension == "connection" {
			label = "max(connection_name)"
		}
		query := fmt.Sprintf(`SELECT %s AS value, %s AS label, count(*) AS count FROM posture_findings WHERE %s
			GROUP BY 1 ORDER BY 3 DESC, 1 LIMIT 500`, column, label, where)
		if err := db.Raw(query, args...).Scan(&values).Error; err != nil {
			return nil, err
		}
		response.Dimensions[dimension] = values
	}
	where, args := filterSQL(projects, filters, "standard")
	var standards []FacetValue
	query := fmt.Sprintf(`SELECT ref->>'standard' AS value, count(DISTINCT id) AS count
		FROM posture_findings, jsonb_array_elements(standards) ref WHERE %s GROUP BY 1 ORDER BY 2 DESC`, where)
	if err := db.Raw(query, args...).Scan(&standards).Error; err != nil {
		return nil, err
	}
	for i := range standards {
		standards[i].Label = spec.PostureStandardName[standards[i].Value]
	}
	response.Dimensions["standard"] = standards
	return response, nil
}

// Finding is one finding with its history.
func (s *Service) Finding(ctx context.Context, projects []uuid.UUID, id uuid.UUID) (*FindingDetail, error) {
	db := storage.GetDb().WithContext(ctx)
	var finding Finding
	err := db.Where("id = ? AND project_id IN ?", id, projects).First(&finding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	detail := &FindingDetail{Finding: finding, Events: []Event{}, StandardNames: map[string]string{}}
	if err := db.Where("finding_id = ?", id).
		Order("at DESC, id DESC").
		Limit(200).
		Find(&detail.Events).
		Error; err != nil {
		return nil, err
	}
	for _, ref := range finding.Standards {
		detail.StandardNames[ref.Standard] = spec.PostureStandardName[ref.Standard]
	}
	if check := baseline.Lookup(finding.CheckID); check != nil {
		detail.Remediation = &check.Remediation
	}
	return detail, nil
}

// Resource is the open findings of one resource, by its cloud ID (the asset drawer's).
func (s *Service) Resource(ctx context.Context, projects []uuid.UUID, key string) ([]Finding, error) {
	key = strings.TrimSpace(key)
	keys := []string{key}
	if strings.HasPrefix(strings.ToLower(key), "/subscriptions/") {
		keys = append(keys, strings.ToLower(key))
	}
	findings := []Finding{}
	err := storage.GetDb().WithContext(ctx).Where("project_id IN ? AND resource_key IN ? AND status = 'open'",
		projects, keys).Order(severityOrder + ", title").Limit(100).Find(&findings).Error
	return findings, err
}

// Coverage is every scope's latest reads: native, fallback to the baseline, or not covered.
func (s *Service) Coverage(ctx context.Context, projects []uuid.UUID) ([]CoverageRow, error) {
	var runs []scopeRun
	if err := storage.GetDb().WithContext(ctx).Where("project_id IN ?", projects).
		Order("connection_name, account, region, standard, source").Find(&runs).Error; err != nil {
		return nil, err
	}
	now := s.now()
	rows := []CoverageRow{}
	index := map[string]int{}
	for _, run := range runs {
		key := run.ConnectionID.String() + "|" + run.Account + "|" + run.Region
		i, ok := index[key]
		if !ok {
			rows = append(rows, CoverageRow{ConnectionID: run.ConnectionID, ConnectionName: run.ConnectionName,
				Provider: run.Provider, Account: run.Account, Region: run.Region, Runs: []CoverageRun{}})
			i = len(rows) - 1
			index[key] = i
		}
		row := &rows[i]
		row.Runs = append(row.Runs, CoverageRun{Standard: run.Standard, Source: run.Source, Result: run.Result,
			Reason: run.Reason, Passed: len(run.Passed), Failed: len(run.Failed)})
		if run.ScannedAt.After(row.ScannedAt) {
			row.ScannedAt = run.ScannedAt
		}
	}
	for i := range rows {
		rows[i].Status, rows[i].Reason = coverageOf(rows[i], now)
	}
	return rows, nil
}

// Trends: open findings at each period's end, opened and resolved within it, and scores.
func (s *Service) Trends(ctx context.Context, projects []uuid.UUID, request TrendsRequest) (*TrendsResponse, error) {
	from, to, err := rangeOf(request.From, request.To)
	if err != nil {
		return nil, err
	}
	if request.Granularity == "daily" && to.Sub(from) >= maxDailySpan*24*time.Hour {
		return nil, fmt.Errorf("daily periods are for ranges of up to %d days", maxDailySpan)
	}
	list, err := periods(from, to, request.Granularity)
	if err != nil {
		return nil, err
	}
	db := storage.GetDb().WithContext(ctx)
	filters := request.Filters
	filters.Status = "all"
	where, args := filterSQL(projects, filters, "")
	response := &TrendsResponse{Periods: make([]TrendPeriod, 0, len(list))}
	for _, p := range list {
		end := p.To.AddDate(0, 0, 1)
		trend := TrendPeriod{Key: p.Key, From: p.From.Format(dayLayout), To: p.To.Format(dayLayout),
			Open: map[string]int{}, Scores: map[string]float64{}}
		var open []struct {
			Severity string
			Count    int
		}
		query := fmt.Sprintf(`SELECT f.severity, count(*) AS count
			FROM (SELECT id, severity FROM posture_findings WHERE %s) f
			JOIN LATERAL (SELECT e.kind FROM posture_finding_events e
				WHERE e.finding_id = f.id AND e.kind IN ('opened', 'reopened', 'resolved') AND e.at < ?
				ORDER BY e.at DESC, e.id DESC LIMIT 1) last ON true
			WHERE last.kind <> 'resolved' GROUP BY 1`, where)
		if err := db.Raw(query, append(slices.Clone(args), end)...).Scan(&open).Error; err != nil {
			return nil, err
		}
		for _, row := range open {
			trend.Open[row.Severity] = row.Count
		}
		var moved []struct {
			Kind  string
			Count int
		}
		query = fmt.Sprintf(`SELECT CASE WHEN e.kind = 'resolved' THEN 'resolved' ELSE 'opened' END AS kind,
			count(*) AS count FROM posture_finding_events e
			JOIN (SELECT id FROM posture_findings WHERE %s) f ON f.id = e.finding_id
			WHERE e.kind IN ('opened', 'reopened', 'resolved') AND e.at >= ? AND e.at < ? GROUP BY 1`, where)
		if err := db.Raw(query, append(slices.Clone(args), p.From, end)...).Scan(&moved).Error; err != nil {
			return nil, err
		}
		for _, row := range moved {
			if row.Kind == "resolved" {
				trend.Resolved = row.Count
			} else {
				trend.Opened = row.Count
			}
		}
		var scores []struct {
			Standard       string
			Passed, Failed int
		}
		if err := db.Raw(`SELECT standard, sum(passed) AS passed, sum(failed) AS failed FROM (
				SELECT DISTINCT ON (connection_id, standard) connection_id, standard, passed, failed
				FROM posture_scores_daily WHERE project_id IN ? AND day <= ?
				ORDER BY connection_id, standard, day DESC) latest GROUP BY 1`,
			projects, p.To.Format(dayLayout)).Scan(&scores).Error; err != nil {
			return nil, err
		}
		for _, score := range scores {
			if score.Passed+score.Failed > 0 {
				trend.Scores[score.Standard] = percent(score.Passed, score.Passed+score.Failed)
			}
		}
		response.Periods = append(response.Periods, trend)
	}
	return response, nil
}

// standardScores scores each standard over the ok runs of the projects (one connection's when
// given): evaluated controls from the runs, failed ones from open, unsuppressed findings.
func standardScores(db *gorm.DB, projects []uuid.UUID, connection *uuid.UUID) ([]StandardScore, error) {
	runs := db.Where("project_id IN ? AND result = ?", projects, spec.PostureRunOK)
	findings := db.Where("project_id IN ? AND status = ? AND NOT suppressed", projects, StatusOpen)
	if connection != nil {
		runs, findings = runs.Where("connection_id = ?", *connection), findings.Where("connection_id = ?", *connection)
	}
	var okRuns []scopeRun
	if err := runs.Find(&okRuns).Error; err != nil {
		return nil, err
	}
	var open []Finding
	if err := findings.Select("standards").Find(&open).Error; err != nil {
		return nil, err
	}
	evaluated := map[string]map[string]bool{}
	sources := map[string]map[string]bool{}
	for _, run := range okRuns {
		if evaluated[run.Standard] == nil {
			evaluated[run.Standard], sources[run.Standard] = map[string]bool{}, map[string]bool{}
		}
		for _, control := range append(slices.Clone([]string(run.Passed)), run.Failed...) {
			evaluated[run.Standard][control] = true
		}
		sources[run.Standard][run.Source] = true
	}
	failed := map[string]map[string]bool{}
	for _, finding := range open {
		for _, ref := range finding.Standards {
			if evaluated[ref.Standard][ref.Control] {
				if failed[ref.Standard] == nil {
					failed[ref.Standard] = map[string]bool{}
				}
				failed[ref.Standard][ref.Control] = true
			}
		}
	}
	scores := make([]StandardScore, 0, len(evaluated))
	for _, standard := range slices.Sorted(maps.Keys(evaluated)) {
		total, bad := len(evaluated[standard]), len(failed[standard])
		scores = append(scores, StandardScore{
			Standard: standard, Name: orDefault(spec.PostureStandardName[standard], standard),
			Passed: total - bad, Failed: bad, Evaluated: total, Percent: percent(total-bad, total),
			Sources: slices.Sorted(maps.Keys(sources[standard])),
		})
	}
	return scores, nil
}

// attachTrends adds the last trendDays' daily scores to each standard.
func attachTrends(db *gorm.DB, projects []uuid.UUID, scores []StandardScore, now time.Time) error {
	var rows []struct {
		Standard       string
		Day            time.Time
		Passed, Failed int
	}
	if err := db.Raw(`SELECT standard, day, sum(passed) AS passed, sum(failed) AS failed FROM posture_scores_daily
		WHERE project_id IN ? AND day > ? GROUP BY 1, 2 ORDER BY 2`,
		projects, now.AddDate(0, 0, -trendDays).Format(dayLayout)).Scan(&rows).Error; err != nil {
		return err
	}
	byStandard := map[string][]ScorePoint{}
	for _, row := range rows {
		if row.Passed+row.Failed > 0 {
			byStandard[row.Standard] = append(byStandard[row.Standard],
				ScorePoint{Day: row.Day.Format(dayLayout), Percent: percent(row.Passed, row.Passed+row.Failed)})
		}
	}
	for i := range scores {
		scores[i].Trend = byStandard[scores[i].Standard]
	}
	return nil
}

// coverageOf: native when a native run is ok; fallback when the baseline covers what native
// couldn't; stale when the last read is too old; otherwise not covered, with the reason.
func coverageOf(row CoverageRow, now time.Time) (string, string) {
	var nativeOK, baselineOK, triedNative bool
	reason := ""
	for _, run := range row.Runs {
		ok := run.Result == spec.PostureRunOK
		switch run.Source {
		case spec.PostureSourceNative:
			triedNative = true
			nativeOK = nativeOK || ok
		case spec.PostureSourceBaseline:
			baselineOK = baselineOK || ok
		}
		if !ok && reason == "" {
			reason = run.Reason
		}
	}
	status := CoverageNotCovered
	switch {
	case nativeOK:
		status, reason = CoverageNative, ""
	case baselineOK && triedNative:
		status = CoverageFallback
	case baselineOK:
		status, reason = CoverageBaseline, ""
	}
	if status != CoverageNotCovered && now.Sub(row.ScannedAt) > staleAfter {
		return CoverageStale, fmt.Sprintf("last read %s", row.ScannedAt.Format(time.RFC3339))
	}
	return status, reason
}

// severityOrder sorts the most severe first.
const severityOrder = `CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2
	WHEN 'low' THEN 3 ELSE 4 END`

// filterSQL is the WHERE clause for filters, leaving out except (a facet's own dimension).
func filterSQL(projects []uuid.UUID, filters Filters, except string) (string, []any) {
	where := []string{"project_id IN ?"}
	args := []any{projects}
	switch filters.Status {
	case "all":
	case "resolved":
		where = append(where, "status = 'resolved'")
	case "suppressed":
		where = append(where, "status = 'open' AND suppressed")
	default:
		where = append(where, "status = 'open' AND NOT suppressed")
	}
	in := func(dimension, column string, values []string) {
		if dimension != except && len(values) > 0 {
			where = append(where, column+" IN ?")
			args = append(args, values)
		}
	}
	in("severity", "severity", filters.Severity)
	in("provider", "provider", filters.Provider)
	in("account", "account", filters.Account)
	in("region", "region", filters.Region)
	in("source", "source", filters.Source)
	in("connection", "connection_id::text", filters.Connection)
	in("control", "control", filters.Control)
	in("resource", "resource_key", filters.Resource)
	if except != "standard" && len(filters.Standard) > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM jsonb_array_elements(standards) ref
			WHERE ref->>'standard' IN ?)`)
		args = append(args, filters.Standard)
	}
	if search := strings.TrimSpace(filters.Search); search != "" {
		where = append(where, "(title ILIKE ? OR control ILIKE ? OR resource_key ILIKE ?)")
		pattern := "%" + strings.NewReplacer("%", `\%`, "_", `\_`).Replace(search) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	return strings.Join(where, " AND "), args
}

func periods(from, to time.Time, granularity string) ([]period, error) {
	var out []period
	for start := from; !start.After(to); {
		var next time.Time
		var key string
		switch granularity {
		case "daily":
			next, key = start.AddDate(0, 0, 1), start.Format(dayLayout)
		case "monthly":
			next, key = time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC), start.Format("2006-01")
		case "quarterly":
			q := (int(start.Month()) - 1) / 3
			next = time.Date(start.Year(), time.Month(q*3+4), 1, 0, 0, 0, 0, time.UTC)
			key = fmt.Sprintf("%d-Q%d", start.Year(), q+1)
		case "yearly":
			next, key = time.Date(start.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC), strconv.Itoa(start.Year())
		default:
			return nil, errors.New("granularity must be daily, monthly, quarterly or yearly")
		}
		end := next.AddDate(0, 0, -1)
		if end.After(to) {
			end = to
		}
		out = append(out, period{Key: key, From: start, To: end})
		start = next
	}
	return out, nil
}

func rangeOf(fromText, toText string) (time.Time, time.Time, error) {
	from, err := time.Parse(dayLayout, strings.TrimSpace(fromText))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%q isn't a day like 2026-11-01", fromText)
	}
	to, err := time.Parse(dayLayout, strings.TrimSpace(toText))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%q isn't a day like 2026-11-01", toText)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("the range ends before it starts")
	}
	if to.Sub(from) > maxRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("a range is at most %d days", maxRangeDays)
	}
	return from, to, nil
}

func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(int(float64(part)/float64(whole)*1000+0.5)) / 10
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
