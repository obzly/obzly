package cloudcost

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

const (
	summaryDays = 90
	// forecastFreshFor: an older forecast isn't added to the month's total.
	forecastFreshFor = 72 * time.Hour
)

type DailyPoint struct {
	Day       string  `json:"day"`
	Amount    float64 `json:"amount"`
	Estimated bool    `json:"estimated,omitempty"`
}

// CurrencySummary is one currency's month so far, last month and the last summaryDays days.
type CurrencySummary struct {
	Currency          string  `json:"currency"`
	MonthToDate       float64 `json:"monthToDate"`
	LastMonth         float64 `json:"lastMonth"`
	LastMonthSameDays float64 `json:"lastMonthSameDays"`
	// Forecast: month to date plus the providers' forecast for the rest (nil without one).
	Forecast *float64     `json:"forecast,omitempty"`
	Daily    []DailyPoint `json:"daily"`
}

// SourceStatus is what one connection's source last delivered.
type SourceStatus struct {
	ProjectID      uuid.UUID  `json:"projectId"`
	ConnectionID   uuid.UUID  `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	Provider       string     `json:"provider"`
	Source         string     `json:"source"`
	FromDay        *string    `json:"from,omitempty"`
	ToDay          *string    `json:"to,omitempty"`
	FinalThrough   *string    `json:"finalThrough,omitempty"`
	CoveredFrom    *string    `json:"coveredFrom,omitempty"`
	CoveredTo      *string    `json:"coveredTo,omitempty"`
	Rows           int        `json:"rows"`
	DeliveredAt    time.Time  `json:"deliveredAt"`
	ForecastAt     *time.Time `json:"forecastAt,omitempty"`
}

type SummaryDTO struct {
	Today         string            `json:"today"`
	Currencies    []CurrencySummary `json:"currencies"`
	Sources       []SourceStatus    `json:"sources"`
	OpenAnomalies int64             `json:"openAnomalies"`
}

// ResourceCost is one resource's cost from the exports (the billing APIs have no single-resource
// cost), by day and by charge category.
type ResourceCost struct {
	Currency string             `json:"currency"`
	Days     []DailyPoint       `json:"days"`
	Total    float64            `json:"total"`
	ByCharge map[string]float64 `json:"byCharge"`
}

type ResourceCostDTO struct {
	Keys       []string       `json:"keys"`
	Currencies []ResourceCost `json:"currencies"`
	// Covered: some export delivered these days for a connection of these projects.
	Covered bool `json:"covered"`
}

func (s *Service) Summary(ctx context.Context, projects []uuid.UUID) (*SummaryDTO, error) {
	today := utcDay(s.now())
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastStart := monthStart.AddDate(0, -1, 0)
	lastSameEnd := lastStart.AddDate(0, 0, today.Day()-1)
	if lastSameEnd.Month() != lastStart.Month() {
		lastSameEnd = monthStart.AddDate(0, 0, -1)
	}
	from := today.AddDate(0, 0, -summaryDays+1)
	if lastStart.Before(from) {
		from = lastStart
	}
	db := storage.GetDb().WithContext(ctx)
	var rows []struct {
		Day       time.Time
		Currency  string
		Amount    float64
		Estimated bool
	}
	if err := db.Raw(`SELECT day, currency, SUM(effective)::float8 AS amount, bool_or(estimated) AS estimated
		FROM cloud_cost_daily WHERE project_id IN ? AND day BETWEEN ? AND ? GROUP BY 1, 2 ORDER BY 1`,
		projects, from.Format(dayLayout), today.Format(dayLayout)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	byCurrency := map[string]*CurrencySummary{}
	dailyFrom := today.AddDate(0, 0, -summaryDays+1)
	for _, row := range rows {
		day := utcDay(row.Day)
		summary := byCurrency[row.Currency]
		if summary == nil {
			summary = &CurrencySummary{Currency: row.Currency, Daily: []DailyPoint{}}
			byCurrency[row.Currency] = summary
		}
		switch {
		case !day.Before(monthStart):
			summary.MonthToDate += row.Amount
		case !day.Before(lastStart):
			summary.LastMonth += row.Amount
			if !day.After(lastSameEnd) {
				summary.LastMonthSameDays += row.Amount
			}
		}
		if !day.Before(dailyFrom) {
			summary.Daily = append(summary.Daily, DailyPoint{Day: day.Format(dayLayout), Amount: row.Amount,
				Estimated: row.Estimated})
		}
	}
	var forecasts []struct {
		Currency  string
		Remaining float64
	}
	if err := db.Raw(`SELECT currency, SUM(remaining)::float8 AS remaining FROM cloud_cost_forecasts
		WHERE project_id IN ? AND month = ? AND updated_at > ? GROUP BY 1`, projects, monthStart.Format(dayLayout),
		s.now().Add(-forecastFreshFor)).Scan(&forecasts).Error; err != nil {
		return nil, err
	}
	for _, forecast := range forecasts {
		summary := byCurrency[forecast.Currency]
		if summary == nil {
			summary = &CurrencySummary{Currency: forecast.Currency, Daily: []DailyPoint{}}
			byCurrency[forecast.Currency] = summary
		}
		total := summary.MonthToDate + forecast.Remaining
		summary.Forecast = &total
	}
	result := &SummaryDTO{Today: today.Format(dayLayout), Currencies: []CurrencySummary{}}
	for _, currency := range sortedKeys(byCurrency) {
		result.Currencies = append(result.Currencies, *byCurrency[currency])
	}
	sources, err := s.Sources(ctx, projects)
	if err != nil {
		return nil, err
	}
	result.Sources = sources
	err = db.Model(&Anomaly{}).Where("project_id IN ? AND status = 'open' AND day >= ?", projects,
		today.AddDate(0, 0, -30).Format(dayLayout)).Count(&result.OpenAnomalies).Error
	return result, err
}

// Sources is what each connection's sources last delivered.
func (s *Service) Sources(ctx context.Context, projects []uuid.UUID) ([]SourceStatus, error) {
	var sources []SourceStatus
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT s.project_id, s.connection_id, s.connection_name, s.provider,
			s.source, to_char(s.from_day, 'YYYY-MM-DD') AS from_day, to_char(s.to_day, 'YYYY-MM-DD') AS to_day,
			to_char(s.final_through, 'YYYY-MM-DD') AS final_through,
			to_char(s.covered_from, 'YYYY-MM-DD') AS covered_from, to_char(s.covered_to, 'YYYY-MM-DD') AS covered_to,
			s.rows, s.delivered_at,
			(SELECT max(f.updated_at) FROM cloud_cost_forecasts f WHERE f.project_id = s.project_id
				AND f.connection_id = s.connection_id) AS forecast_at
		FROM cloud_cost_sources s WHERE s.project_id IN ? ORDER BY lower(s.connection_name), s.source`,
		projects).Scan(&sources).Error
	if sources == nil {
		sources = []SourceStatus{}
	}
	return sources, err
}

// Resource is the cost of the resource named by key over the last days, from exports.
func (s *Service) Resource(ctx context.Context, projects []uuid.UUID, key string, days int) (*ResourceCostDTO, error) {
	days = min(max(days, 1), 366)
	today := utcDay(s.now())
	from := today.AddDate(0, 0, -days+1)
	keys := resourceKeys(key)
	db := storage.GetDb().WithContext(ctx)
	var rows []struct {
		Day            time.Time
		Currency       string
		ChargeCategory string
		Amount         float64
		Estimated      bool
	}
	if err := db.Raw(`SELECT day, currency, charge_category, SUM(effective)::float8 AS amount,
			bool_or(estimated) AS estimated FROM cloud_cost_daily
		WHERE project_id IN ? AND source = 'export' AND resource IN ? AND day BETWEEN ? AND ?
		GROUP BY 1, 2, 3 ORDER BY 1`, projects, keys, from.Format(dayLayout), today.Format(dayLayout)).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	byCurrency := map[string]*ResourceCost{}
	dayIndex := map[string]map[string]int{}
	for _, row := range rows {
		cost := byCurrency[row.Currency]
		if cost == nil {
			cost = &ResourceCost{Currency: row.Currency, ByCharge: map[string]float64{}}
			byCurrency[row.Currency] = cost
			dayIndex[row.Currency] = map[string]int{}
		}
		day := utcDay(row.Day).Format(dayLayout)
		i, ok := dayIndex[row.Currency][day]
		if !ok {
			cost.Days = append(cost.Days, DailyPoint{Day: day})
			i = len(cost.Days) - 1
			dayIndex[row.Currency][day] = i
		}
		cost.Days[i].Amount += row.Amount
		cost.Days[i].Estimated = cost.Days[i].Estimated || row.Estimated
		cost.Total += row.Amount
		cost.ByCharge[firstNonEmpty(row.ChargeCategory, "Usage")] += row.Amount
	}
	result := &ResourceCostDTO{Keys: keys, Currencies: []ResourceCost{}}
	for _, currency := range sortedKeys(byCurrency) {
		result.Currencies = append(result.Currencies, *byCurrency[currency])
	}
	// Only an export of the resource's own cloud can have it: an AWS export says nothing about an
	// Azure VM.
	var covered int64
	err := db.Raw(`SELECT count(*) FROM cloud_cost_sources WHERE project_id IN ? AND source = 'export'
		AND provider = ? AND covered_to >= ?`, projects, resourceProvider(keys[0]), from.Format(dayLayout)).
		Scan(&covered).Error
	result.Covered = covered > 0
	return result, err
}

var (
	awsInstanceARN = regexp.MustCompile(`^arn:aws[a-z-]*:ec2:[^:]*:\d{12}:instance/(i-[0-9a-f]+)$`)
	// awsEC2ResourceARN: volumes, Elastic IPs and the like, which billing names by their ID.
	awsEC2ResourceARN = regexp.MustCompile(`^arn:aws[a-z-]*:ec2:[^:]*:\d{12}:[a-z-]+/([a-z]+-[0-9a-f]+)$`)
)

// resourceKeys are the ways billing names a resource inventory knows by its cloud ID: an EC2
// instance by its ID, an S3 bucket by its name, an Azure resource by its lowercased ID.
func resourceKeys(key string) []string {
	key = strings.TrimSpace(key)
	keys := []string{key}
	if strings.HasPrefix(strings.ToLower(key), "/subscriptions/") {
		return []string{strings.ToLower(key)}
	}
	if match := awsInstanceARN.FindStringSubmatch(key); match != nil {
		keys = append(keys, match[1])
	} else if match := awsEC2ResourceARN.FindStringSubmatch(key); match != nil {
		keys = append(keys, match[1])
	}
	if bucket, ok := strings.CutPrefix(key, "arn:aws:s3:::"); ok && bucket != "" {
		keys = append(keys, bucket)
	}
	return keys
}

// resourceProvider tells an Azure resource ID (/subscriptions/...) from an AWS ARN or ID.
func resourceProvider(key string) string {
	if strings.HasPrefix(key, "/subscriptions/") {
		return spec.ProviderAzure
	}
	return spec.ProviderAWS
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
