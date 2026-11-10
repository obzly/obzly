package cloudcost

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"

	"obzly/internal/storage"
)

const (
	// anomalyDays: the recent days checked after each delivery.
	anomalyDays = 7
	// anomalyWeeks of the same weekday make the baseline; at least anomalyMinWeeks are needed.
	anomalyWeeks    = 8
	anomalyMinWeeks = 4
	anomalyMinScore = 3.5
	// A day is flagged only if it differs by at least anomalyMinAmount (in its currency) and
	// anomalyMinShare of the usual.
	anomalyMinAmount = 50
	anomalyMinShare  = 0.10
	// madScale makes the median absolute deviation comparable to a standard deviation.
	madScale = 0.6745
	maxScore = 99
)

type seriesKey struct {
	subAccount, service, currency string
}

type dayValue struct {
	amount    float64
	estimated bool
}

// detectAnomalies checks a connection's last anomalyDays days, per service and per account, against
// the same weekday of the weeks before; restated days that no longer stand out are cleared.
func (s *Service) detectAnomalies(ctx context.Context, projectID, connectionID uuid.UUID, now time.Time) error {
	today := utcDay(now)
	start := today.AddDate(0, 0, -anomalyDays-7*anomalyWeeks)
	db := storage.GetDb().WithContext(ctx)
	var rows []struct {
		Day        time.Time
		SubAccount string
		Service    string
		Currency   string
		Amount     float64
		Estimated  bool
	}
	err := db.Raw(`SELECT day, sub_account, service, currency, SUM(effective)::float8 AS amount,
			bool_or(estimated) AS estimated FROM cloud_cost_daily
		WHERE project_id = ? AND connection_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2, 3, 4
		UNION ALL
		SELECT day, sub_account, '' AS service, currency, SUM(effective)::float8, bool_or(estimated)
		FROM cloud_cost_daily WHERE project_id = ? AND connection_id = ? AND day BETWEEN ? AND ? GROUP BY 1, 2, 4`,
		projectID, connectionID, start.Format(dayLayout), today.Format(dayLayout),
		projectID, connectionID, start.Format(dayLayout), today.Format(dayLayout)).Scan(&rows).Error
	if err != nil {
		return err
	}
	series := map[seriesKey]map[string]dayValue{}
	for _, row := range rows {
		key := seriesKey{row.SubAccount, row.Service, row.Currency}
		if series[key] == nil {
			series[key] = map[string]dayValue{}
		}
		series[key][utcDay(row.Day).Format(dayLayout)] = dayValue{row.Amount, row.Estimated}
	}
	found := map[string]Anomaly{}
	for key, days := range series {
		for back := 1; back <= anomalyDays; back++ {
			day := today.AddDate(0, 0, -back)
			if anomaly, ok := checkDay(days, day); ok {
				anomaly.ProjectID, anomaly.ConnectionID = projectID, connectionID
				anomaly.SubAccount, anomaly.Service, anomaly.Currency = key.subAccount, key.service, key.currency
				found[anomalyKey(anomaly)] = anomaly
			}
		}
	}
	for _, anomaly := range found {
		err := db.Exec(`INSERT INTO cost_anomalies (project_id, connection_id, day, sub_account, service, currency,
				expected, actual, score) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, connection_id, day, sub_account, service, currency) DO UPDATE SET
				expected = EXCLUDED.expected, actual = EXCLUDED.actual, score = EXCLUDED.score`,
			projectID, connectionID, anomaly.Day, anomaly.SubAccount, anomaly.Service, anomaly.Currency,
			anomaly.Expected, anomaly.Actual, anomaly.Score).Error
		if err != nil {
			return err
		}
	}
	var open []Anomaly
	if err := db.Where("project_id = ? AND connection_id = ? AND status = 'open' AND day BETWEEN ? AND ?",
		projectID, connectionID, today.AddDate(0, 0, -anomalyDays).Format(dayLayout),
		today.AddDate(0, 0, -1).Format(dayLayout)).Find(&open).Error; err != nil {
		return err
	}
	for _, anomaly := range open {
		anomaly.Day = dayText(anomaly.Day)
		if _, still := found[anomalyKey(anomaly)]; !still {
			if err := db.Delete(&Anomaly{}, "id = ?", anomaly.ID).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// checkDay is day's anomaly, if its cost is far from the median of the same weekday before it.
func checkDay(days map[string]dayValue, day time.Time) (Anomaly, bool) {
	value, ok := days[day.Format(dayLayout)]
	if !ok {
		return Anomaly{}, false
	}
	var baseline []float64
	for week := 1; week <= anomalyWeeks; week++ {
		if before, ok := days[day.AddDate(0, 0, -7*week).Format(dayLayout)]; ok {
			baseline = append(baseline, before.amount)
		}
	}
	if len(baseline) < anomalyMinWeeks {
		return Anomaly{}, false
	}
	median := medianOf(baseline)
	deviations := make([]float64, len(baseline))
	for i, v := range baseline {
		deviations[i] = math.Abs(v - median)
	}
	mad := medianOf(deviations)
	diff := value.amount - median
	if math.Abs(diff) < math.Max(anomalyMinAmount, anomalyMinShare*math.Abs(median)) {
		return Anomaly{}, false
	}
	// A day not final yet often looks low; only rises count on it.
	if diff < 0 && value.estimated {
		return Anomaly{}, false
	}
	score := maxScore * math.Copysign(1, diff)
	if mad > 0 {
		score = math.Max(-maxScore, math.Min(maxScore, madScale*diff/mad))
	}
	if math.Abs(score) < anomalyMinScore {
		return Anomaly{}, false
	}
	return Anomaly{Day: day.Format(dayLayout), Expected: median, Actual: value.amount,
		Score: math.Round(score*10) / 10}, true
}

func medianOf(values []float64) float64 {
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func anomalyKey(a Anomaly) string {
	return a.Day + "|" + a.SubAccount + "|" + a.Service + "|" + a.Currency
}

// dayText is a DATE column read back as text ("2026-11-08" or "2026-11-08T00:00:00Z").
func dayText(value string) string {
	if len(value) >= len(dayLayout) {
		return value[:len(dayLayout)]
	}
	return value
}

// AnomalyDTO adds names to an anomaly.
type AnomalyDTO struct {
	Anomaly
	ConnectionName string `json:"connectionName"`
	SubAccountName string `json:"subAccountName,omitempty"`
}

// ListAnomalies is projects' anomalies from..to, newest first.
func (s *Service) ListAnomalies(
	ctx context.Context, projects []uuid.UUID, from, to time.Time, status string,
) ([]AnomalyDTO, error) {
	query := storage.GetDb().WithContext(ctx).Where("project_id IN ? AND day BETWEEN ? AND ?", projects,
		from.Format(dayLayout), to.Format(dayLayout))
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var anomalies []Anomaly
	if err := query.Order("day DESC, abs(actual - expected) DESC").Limit(1000).Find(&anomalies).Error; err != nil {
		return nil, err
	}
	names, err := s.connectionNames(ctx, projects)
	if err != nil {
		return nil, err
	}
	accounts, err := s.labels(ctx, projects, DimAccount)
	if err != nil {
		return nil, err
	}
	result := make([]AnomalyDTO, 0, len(anomalies))
	for _, anomaly := range anomalies {
		anomaly.Day = dayText(anomaly.Day)
		result = append(result, AnomalyDTO{Anomaly: anomaly, ConnectionName: names[anomaly.ConnectionID],
			SubAccountName: accounts[anomaly.SubAccount]})
	}
	return result, nil
}

// UpdateAnomaly acknowledges or reopens an anomaly, with a note.
func (s *Service) UpdateAnomaly(ctx context.Context, id uuid.UUID, status, note string) (*Anomaly, error) {
	if status != "open" && status != "acknowledged" {
		return nil, errors.New("status must be open or acknowledged")
	}
	if len(note) > 1000 {
		return nil, errors.New("a note is at most 1000 characters")
	}
	db := storage.GetDb().WithContext(ctx)
	if err := db.Model(&Anomaly{}).Where("id = ?", id).Updates(map[string]any{"status": status, "note": note}).
		Error; err != nil {
		return nil, err
	}
	var anomaly Anomaly
	if err := db.Where("id = ?", id).First(&anomaly).Error; err != nil {
		return nil, ErrNotFound
	}
	anomaly.Day = dayText(anomaly.Day)
	return &anomaly, nil
}

func (s *Service) anomalyProject(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	var anomaly Anomaly
	if err := storage.GetDb().
		WithContext(ctx).
		Select("project_id").
		Where("id = ?", id).
		First(&anomaly).
		Error; err != nil {
		return uuid.Nil, ErrNotFound
	}
	return anomaly.ProjectID, nil
}

func (s *Service) connectionNames(ctx context.Context, projects []uuid.UUID) (map[uuid.UUID]string, error) {
	var rows []struct {
		ConnectionID   uuid.UUID
		ConnectionName string
	}
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT connection_id, max(connection_name) AS connection_name
		FROM cloud_cost_sources WHERE project_id IN ? GROUP BY 1`, projects).Scan(&rows).Error
	names := map[uuid.UUID]string{}
	for _, row := range rows {
		names[row.ConnectionID] = row.ConnectionName
	}
	return names, err
}
