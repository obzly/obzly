// Package cloudcost keeps a Site's cloud cost (docs/CLOUD_COST_POSTURE.md "Cost"): the probe's
// daily FOCUS rows per connection, forecasts, budgets and anomalies, and the Cost tab's queries.
package cloudcost

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	dayLayout = "2006-01-02"

	GranularityDaily     = "daily"
	GranularityMonthly   = "monthly"
	GranularityQuarterly = "quarterly"
	GranularityYearly    = "yearly"

	MetricEffective = "effective"
	MetricBilled    = "billed"

	// Group-by dimensions; a tracked tag is "tag:<key>".
	DimProvider = "provider"
	DimAccount  = "account"
	DimRegion   = "region"
	DimService  = "service"
	DimResource = "resource"
	DimCharge   = "charge"
	tagPrefix   = "tag:"

	CoverageFull    = "full"
	CoveragePartial = "partial"
	CoverageNone    = "none"

	// maxDailySpan: daily periods only for ranges up to this many days.
	maxDailySpan = 92
	// maxRangeDays bounds any query's range (a little over 5 years).
	maxRangeDays = 1900
	// maxExploreRows: the groups returned per currency; the rest add up as "other".
	maxExploreRows = 500
)

var (
	ErrForbidden = errors.New("you can't see this project's cost")
	ErrNotFound  = errors.New("not found")
)

// costDaily is one stored row (amounts are NUMERIC; written as decimal text).
type costDaily struct {
	ProjectID       uuid.UUID `gorm:"column:project_id"`
	ConnectionID    uuid.UUID `gorm:"column:connection_id"`
	Source          string    `gorm:"column:source"`
	Day             string    `gorm:"column:day"`
	Provider        string    `gorm:"column:provider"`
	BillingAccount  string    `gorm:"column:billing_account"`
	SubAccount      string    `gorm:"column:sub_account"`
	SubAccountName  string    `gorm:"column:sub_account_name"`
	Service         string    `gorm:"column:service"`
	Region          string    `gorm:"column:region"`
	Resource        string    `gorm:"column:resource"`
	ChargeCategory  string    `gorm:"column:charge_category"`
	PricingCategory string    `gorm:"column:pricing_category"`
	Currency        string    `gorm:"column:currency"`
	Billed          string    `gorm:"column:billed"`
	Effective       string    `gorm:"column:effective"`
	List            string    `gorm:"column:list"`
	Tags            jsonMap   `gorm:"column:tags"`
	Estimated       bool      `gorm:"column:estimated"`
}

func (costDaily) TableName() string { return "cloud_cost_daily" }

// Budget is a monthly amount in one currency over the project's cost, optionally filtered.
type Budget struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey"     json:"id"`
	ProjectID        uuid.UUID  `gorm:"column:project_id"        json:"projectId"`
	Name             string     `gorm:"column:name"              json:"name"`
	Filters          Filters    `gorm:"column:filters"           json:"filters"`
	Currency         string     `gorm:"column:currency"          json:"currency"`
	Amount           float64    `gorm:"column:amount"            json:"amount"`
	ThresholdPercent int        `gorm:"column:threshold_percent" json:"thresholdPercent"`
	ChannelIDs       uuidList   `gorm:"column:channel_ids"       json:"channelIds"`
	CreatedBy        *uuid.UUID `gorm:"column:created_by"        json:"-"`
	CreatedAt        time.Time  `gorm:"column:created_at"        json:"createdAt"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"        json:"updatedAt"`
}

func (Budget) TableName() string { return "cost_budgets" }

// Anomaly is a day whose cost for a service (or a whole account: Service "") was far from the
// usual for that weekday.
type Anomaly struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey" json:"id"`
	ProjectID    uuid.UUID `gorm:"column:project_id"    json:"projectId"`
	ConnectionID uuid.UUID `gorm:"column:connection_id" json:"connectionId"`
	Day          string    `gorm:"column:day"           json:"day"`
	SubAccount   string    `gorm:"column:sub_account"   json:"subAccount"`
	Service      string    `gorm:"column:service"       json:"service"`
	Currency     string    `gorm:"column:currency"      json:"currency"`
	Expected     float64   `gorm:"column:expected"      json:"expected"`
	Actual       float64   `gorm:"column:actual"        json:"actual"`
	Score        float64   `gorm:"column:score"         json:"score"`
	Status       string    `gorm:"column:status"        json:"status"`
	Note         string    `gorm:"column:note"          json:"note"`
	DetectedAt   time.Time `gorm:"column:detected_at"   json:"detectedAt"`
}

func (Anomaly) TableName() string { return "cost_anomalies" }

// alertSettings is where a project's new anomalies are sent.
type alertSettings struct {
	ProjectID         uuid.UUID `gorm:"column:project_id;primaryKey"`
	AnomalyChannelIDs uuidList  `gorm:"column:anomaly_channel_ids"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (alertSettings) TableName() string { return "cost_alert_settings" }

// Filters narrow cost: each list keeps rows whose value is one of those given ("" is a row
// without the value, e.g. untagged); Tags is per tracked tag key.
type Filters struct {
	Provider []string            `json:"provider,omitempty"`
	Account  []string            `json:"account,omitempty"`
	Region   []string            `json:"region,omitempty"`
	Service  []string            `json:"service,omitempty"`
	Resource []string            `json:"resource,omitempty"`
	Charge   []string            `json:"charge,omitempty"`
	Tags     map[string][]string `json:"tags,omitempty"`
}

func (f Filters) Value() (driver.Value, error) { return jsonValue(f) }
func (f *Filters) Scan(value any) error        { return scanJSON(value, f) }

type jsonMap map[string]string

func (m jsonMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return jsonValue(m)
}
func (m *jsonMap) Scan(value any) error { return scanJSON(value, m) }

type uuidList []uuid.UUID

func (l uuidList) Value() (driver.Value, error) {
	if l == nil {
		return "[]", nil
	}
	return jsonValue(l)
}
func (l *uuidList) Scan(value any) error { return scanJSON(value, l) }

func jsonValue(v any) (driver.Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func scanJSON(value any, target any) error {
	switch typed := value.(type) {
	case []byte:
		return json.Unmarshal(typed, target)
	case string:
		return json.Unmarshal([]byte(typed), target)
	case nil:
		return nil
	default:
		return fmt.Errorf("unsupported JSON column type %T", value)
	}
}

// microsText is millionths as decimal text for a NUMERIC column, exactly.
func microsText(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	return sign + strconv.FormatInt(micros/1_000_000, 10) + "." + fmt.Sprintf("%06d", micros%1_000_000)
}

// period is one column of a cost or trend query: Key is what SQL groups by.
type period struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// periods splits from..to (inclusive days) by granularity; the first and last can be cut short.
func periods(from, to time.Time, granularity string) []period {
	var out []period
	for start := from; !start.After(to); {
		var next time.Time
		switch granularity {
		case GranularityDaily:
			next = start.AddDate(0, 0, 1)
		case GranularityMonthly:
			next = time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case GranularityQuarterly:
			q := (int(start.Month()) - 1) / 3
			next = time.Date(start.Year(), time.Month(q*3+4), 1, 0, 0, 0, 0, time.UTC)
		default:
			next = time.Date(start.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		end := next.AddDate(0, 0, -1)
		if end.After(to) {
			end = to
		}
		out = append(
			out,
			period{Key: periodKey(start, granularity), From: start.Format(dayLayout), To: end.Format(dayLayout)},
		)
		start = next
	}
	return out
}

// periodKey matches periodSQL's to_char for the same day.
func periodKey(day time.Time, granularity string) string {
	switch granularity {
	case GranularityDaily:
		return day.Format(dayLayout)
	case GranularityMonthly:
		return day.Format("2006-01")
	case GranularityQuarterly:
		return fmt.Sprintf("%d-Q%d", day.Year(), (int(day.Month())-1)/3+1)
	}
	return strconv.Itoa(day.Year())
}

func periodSQL(granularity string) string {
	switch granularity {
	case GranularityDaily:
		return `to_char(day, 'YYYY-MM-DD')`
	case GranularityMonthly:
		return `to_char(day, 'YYYY-MM')`
	case GranularityQuarterly:
		return `to_char(day, 'YYYY-"Q"Q')`
	}
	return `to_char(day, 'YYYY')`
}

func parseDay(value string) (time.Time, error) {
	day, err := time.Parse(dayLayout, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q isn't a day like 2026-11-01", value)
	}
	return day, nil
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
