package cloudcost

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"obzly/internal/storage"
)

// ExploreRequest is a Cost Explorer query: the range, its periods, what to group by and filter.
type ExploreRequest struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Granularity string  `json:"granularity"`
	GroupBy     string  `json:"groupBy"`
	Metric      string  `json:"metric"`
	Filters     Filters `json:"filters"`
	// ProjectID narrows to one project (else every project the user can read).
	ProjectID string `json:"projectId,omitempty"`
}

// PeriodDTO is a column: Coverage says whether every connection in scope had data for all of it
// (full, partial or none; empty when no connection is in scope).
type PeriodDTO struct {
	Key      string `json:"key"`
	From     string `json:"from"`
	To       string `json:"to"`
	Coverage string `json:"coverage"`
}

type ExploreRow struct {
	Key    string    `json:"key"`
	Label  string    `json:"label,omitempty"`
	Values []float64 `json:"values"`
	Total  float64   `json:"total"`
}

// CurrencyBlock is one currency's rows: currencies are never added together.
type CurrencyBlock struct {
	Currency string       `json:"currency"`
	Rows     []ExploreRow `json:"rows"`
	// Other: the groups past the first maxExploreRows, added up.
	Other  *ExploreRow `json:"other,omitempty"`
	Totals []float64   `json:"totals"`
	Total  float64     `json:"total"`
}

type ExploreResponse struct {
	Periods    []PeriodDTO     `json:"periods"`
	Currencies []CurrencyBlock `json:"currencies"`
}

// FacetValue is one value a filter can take with the other filters applied, with its effective
// cost per currency (never added together).
type FacetValue struct {
	Value   string             `json:"value"`
	Label   string             `json:"label,omitempty"`
	Amounts map[string]float64 `json:"amounts"`
}

type FacetsResponse struct {
	Dimensions map[string][]FacetValue `json:"dimensions"`
}

// Explore sums cost by group and period.
func (s *Service) Explore(ctx context.Context, projects []uuid.UUID, request ExploreRequest) (*ExploreResponse, error) {
	from, to, err := rangeOf(request.From, request.To)
	if err != nil {
		return nil, err
	}
	granularity := request.Granularity
	switch granularity {
	case GranularityDaily:
		if to.Sub(from) >= maxDailySpan*24*time.Hour {
			return nil, fmt.Errorf("daily periods are for ranges of up to %d days", maxDailySpan)
		}
	case GranularityMonthly, GranularityQuarterly, GranularityYearly:
	default:
		return nil, errors.New("granularity must be daily, monthly, quarterly or yearly")
	}
	group, err := groupSQL(request.GroupBy)
	if err != nil {
		return nil, err
	}
	metric, err := metricColumn(request.Metric)
	if err != nil {
		return nil, err
	}
	resourceLevel := request.GroupBy == DimResource || len(request.Filters.Resource) > 0
	where, args := filterSQL(projects, from, to, request.Filters, "", resourceLevel)
	var rows []struct {
		G        string
		P        string
		Currency string
		Amount   float64
	}
	query := fmt.Sprintf(`SELECT %s AS g, %s AS p, currency, SUM(%s)::float8 AS amount FROM cloud_cost_daily
		WHERE %s GROUP BY 1, 2, 3`, group, periodSQL(granularity), metric, where)
	if err := storage.GetDb().WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	columns := periods(from, to, granularity)
	index := map[string]int{}
	response := &ExploreResponse{Periods: make([]PeriodDTO, len(columns)), Currencies: []CurrencyBlock{}}
	for i, p := range columns {
		index[p.Key] = i
		response.Periods[i] = PeriodDTO{Key: p.Key, From: p.From, To: p.To}
	}
	if err := s.fillCoverage(ctx, projects, request.Filters, resourceLevel, response.Periods); err != nil {
		return nil, err
	}
	byCurrency := map[string]map[string]*ExploreRow{}
	for _, row := range rows {
		i, ok := index[row.P]
		if !ok {
			continue
		}
		groups := byCurrency[row.Currency]
		if groups == nil {
			groups = map[string]*ExploreRow{}
			byCurrency[row.Currency] = groups
		}
		group := groups[row.G]
		if group == nil {
			group = &ExploreRow{Key: row.G, Values: make([]float64, len(columns))}
			groups[row.G] = group
		}
		group.Values[i] += row.Amount
		group.Total += row.Amount
	}
	labels, err := s.labels(ctx, projects, request.GroupBy)
	if err != nil {
		return nil, err
	}
	for _, currency := range sortedKeys(byCurrency) {
		block := CurrencyBlock{Currency: currency, Totals: make([]float64, len(columns))}
		list := make([]ExploreRow, 0, len(byCurrency[currency]))
		for _, row := range byCurrency[currency] {
			row.Label = labels[row.Key]
			list = append(list, *row)
			for i, v := range row.Values {
				block.Totals[i] += v
			}
			block.Total += row.Total
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Total != list[j].Total {
				return list[i].Total > list[j].Total
			}
			return list[i].Key < list[j].Key
		})
		if len(list) > maxExploreRows {
			other := ExploreRow{Key: "", Label: fmt.Sprintf("%d more", len(list)-maxExploreRows),
				Values: make([]float64, len(columns))}
			for _, row := range list[maxExploreRows:] {
				for i, v := range row.Values {
					other.Values[i] += v
				}
				other.Total += row.Total
			}
			block.Other = &other
			list = list[:maxExploreRows]
		}
		block.Rows = list
		response.Currencies = append(response.Currencies, block)
	}
	return response, nil
}

// Facets lists, per filter, the values it can take with the other filters applied: picking Azure
// leaves only Azure accounts, regions, services, resources and tags.
func (s *Service) Facets(ctx context.Context, projects []uuid.UUID, request ExploreRequest) (*FacetsResponse, error) {
	from, to, err := rangeOf(request.From, request.To)
	if err != nil {
		return nil, err
	}
	dimensions := []string{DimProvider, DimAccount, DimRegion, DimService, DimResource, DimCharge}
	tagKeys, err := s.tagKeys(ctx, projects, from, to)
	if err != nil {
		return nil, err
	}
	for _, key := range tagKeys {
		dimensions = append(dimensions, tagPrefix+key)
	}
	labels, err := s.labels(ctx, projects, DimAccount)
	if err != nil {
		return nil, err
	}
	response := &FacetsResponse{Dimensions: map[string][]FacetValue{}}
	for _, dimension := range dimensions {
		group, _ := groupSQL(dimension)
		resourceLevel := dimension == DimResource || len(request.Filters.Resource) > 0
		where, args := filterSQL(projects, from, to, request.Filters, dimension, resourceLevel)
		limit := 1000
		if dimension == DimResource {
			where += " AND resource <> ''"
			limit = 500
		}
		// The values with the most cost in any currency come first.
		var rows []struct {
			Value    string
			Currency string
			Amount   float64
		}
		query := fmt.Sprintf(`WITH sums AS (
				SELECT %s AS value, currency, SUM(effective)::float8 AS amount FROM cloud_cost_daily WHERE %s
				GROUP BY 1, 2),
			top AS (SELECT value, max(abs(amount)) AS size FROM sums GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT %d)
			SELECT sums.value, sums.currency, sums.amount FROM sums JOIN top USING (value)
			ORDER BY top.size DESC, sums.value, sums.amount DESC`, group, where, limit)
		if err := storage.GetDb().WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
			return nil, err
		}
		values := []FacetValue{}
		for _, row := range rows {
			if len(values) == 0 || values[len(values)-1].Value != row.Value {
				values = append(values, FacetValue{Value: row.Value, Amounts: map[string]float64{}})
				if dimension == DimAccount {
					values[len(values)-1].Label = labels[row.Value]
				}
			}
			values[len(values)-1].Amounts[row.Currency] = row.Amount
		}
		response.Dimensions[dimension] = values
	}
	return response, nil
}

// labels names a dimension's values: account IDs by their names.
func (s *Service) labels(ctx context.Context, projects []uuid.UUID, dimension string) (map[string]string, error) {
	labels := map[string]string{}
	if dimension != DimAccount {
		return labels, nil
	}
	var rows []struct {
		SubAccount     string
		SubAccountName string
	}
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT sub_account, max(sub_account_name) AS sub_account_name
		FROM cloud_cost_daily WHERE project_id IN ? AND sub_account_name <> '' GROUP BY 1`, projects).Scan(&rows).Error
	for _, row := range rows {
		labels[row.SubAccount] = row.SubAccountName
	}
	return labels, err
}

func (s *Service) tagKeys(ctx context.Context, projects []uuid.UUID, from, to time.Time) ([]string, error) {
	var keys []string
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT DISTINCT jsonb_object_keys(tags) AS key FROM cloud_cost_daily
		WHERE project_id IN ? AND day BETWEEN ? AND ? AND tags <> '{}'::jsonb ORDER BY 1 LIMIT 20`,
		projects, from.Format(dayLayout), to.Format(dayLayout)).Scan(&keys).Error
	return keys, err
}

// coverageSource is the days one connection's source has delivered.
type coverageSource struct {
	ConnectionID uuid.UUID
	Provider     string
	Source       string
	CoveredFrom  *time.Time
	CoveredTo    *time.Time
}

// fillCoverage marks each period full, partial or none: the share of (connection, day) pairs in
// scope that some source delivered. Single resources come from exports only.
func (s *Service) fillCoverage(
	ctx context.Context, projects []uuid.UUID, filters Filters, resourceLevel bool, out []PeriodDTO,
) error {
	var sources []coverageSource
	query := storage.GetDb().WithContext(ctx).Table("cloud_cost_sources").
		Select("connection_id, provider, source, covered_from, covered_to").Where("project_id IN ?", projects)
	if len(filters.Provider) > 0 {
		query = query.Where("provider IN ?", filters.Provider)
	}
	if resourceLevel {
		query = query.Where("source = 'export'")
	}
	if err := query.Scan(&sources).Error; err != nil {
		return err
	}
	type span struct{ from, to time.Time }
	spans := map[uuid.UUID][]span{}
	for _, source := range sources {
		if _, ok := spans[source.ConnectionID]; !ok {
			spans[source.ConnectionID] = nil
		}
		if source.CoveredFrom != nil && source.CoveredTo != nil {
			spans[source.ConnectionID] = append(spans[source.ConnectionID],
				span{utcDay(*source.CoveredFrom), utcDay(*source.CoveredTo)})
		}
	}
	for i := range out {
		from, _ := parseDay(out[i].From)
		to, _ := parseDay(out[i].To)
		days := int(to.Sub(from)/(24*time.Hour)) + 1
		total, covered := 0, 0
		for _, list := range spans {
			total += days
			for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
				for _, sp := range list {
					if !day.Before(sp.from) && !day.After(sp.to) {
						covered++
						break
					}
				}
			}
		}
		switch {
		case total == 0:
			// No connection in scope here: another Site may have them, so this one says nothing.
			out[i].Coverage = ""
		case covered == 0:
			out[i].Coverage = CoverageNone
		case covered < total:
			out[i].Coverage = CoveragePartial
		default:
			out[i].Coverage = CoverageFull
		}
	}
	return nil
}

// filterSQL is the WHERE clause for projects, from..to and filters, leaving out the skip
// dimension's own filter (facets). Resource-level queries read exports only: the billing APIs
// have no single-resource cost.
func filterSQL(
	projects []uuid.UUID, from, to time.Time, f Filters, skip string, resourceLevel bool,
) (string, []any) {
	clauses := []string{"project_id IN ?", "day BETWEEN ? AND ?"}
	args := []any{projects, from.Format(dayLayout), to.Format(dayLayout)}
	if resourceLevel {
		clauses = append(clauses, "source = 'export'")
	}
	add := func(dimension, column string, values []string) {
		if dimension == skip || len(values) == 0 {
			return
		}
		clauses = append(clauses, column+" IN ?")
		args = append(args, values)
	}
	add(DimProvider, "provider", f.Provider)
	add(DimAccount, "sub_account", f.Account)
	add(DimRegion, "region", f.Region)
	add(DimService, "service", f.Service)
	add(DimResource, "resource", f.Resource)
	add(DimCharge, "charge_category", f.Charge)
	for _, key := range sortedKeys(f.Tags) {
		values := f.Tags[key]
		if tagPrefix+key == skip || len(values) == 0 {
			continue
		}
		clause := "COALESCE(tags->>?, '') IN ?"
		clauses = append(clauses, clause)
		args = append(args, key, values)
	}
	return strings.Join(clauses, " AND "), args
}

func groupSQL(dimension string) (string, error) {
	switch dimension {
	case DimProvider:
		return "provider", nil
	case DimAccount:
		return "sub_account", nil
	case DimRegion:
		return "region", nil
	case DimService:
		return "service", nil
	case DimResource:
		return "resource", nil
	case DimCharge:
		return "charge_category", nil
	}
	// Tag keys go into the SQL text, so only the characters cloud tag keys use are allowed.
	if key, ok := strings.CutPrefix(dimension, tagPrefix); ok && tagKeyPattern.MatchString(key) {
		return "COALESCE(tags->>'" + key + "', '')", nil
	}
	return "", fmt.Errorf("can't group by %q", dimension)
}

func metricColumn(metric string) (string, error) {
	switch metric {
	case "", MetricEffective:
		return "effective", nil
	case MetricBilled:
		return "billed", nil
	}
	return "", errors.New("metric must be effective or billed")
}

func rangeOf(fromText, toText string) (time.Time, time.Time, error) {
	from, err := parseDay(fromText)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseDay(toText)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("the range ends before it starts")
	}
	if to.Sub(from) > maxRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("a range is at most %d days", maxRangeDays)
	}
	return from, to, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var tagKeyPattern = regexp.MustCompile(`^[\p{L}\p{N} _.:/=+@-]{1,128}$`)
