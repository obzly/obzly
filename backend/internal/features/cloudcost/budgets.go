package cloudcost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"obzly/internal/features/alerting"
	"obzly/internal/storage"
)

const (
	budgetRuleInterval  = 3600
	budgetRuleRepeat    = 24 * 3600
	anomalyRuleInterval = 900
	// commitmentRuleInterval: end dates are days away; checking hourly is plenty.
	commitmentRuleInterval = 3600
	anomalyAlertLookback   = 36 * time.Hour
	// runRateDays: a filtered budget projects its month from the last days' average.
	runRateDays = 7

	BudgetOK   = "ok"
	BudgetNear = "near"
	BudgetOver = "over"
)

// BudgetRequest creates or changes a budget.
type BudgetRequest struct {
	Name             string      `json:"name"`
	Filters          Filters     `json:"filters"`
	Currency         string      `json:"currency"`
	Amount           float64     `json:"amount"`
	ThresholdPercent int         `json:"thresholdPercent"`
	ChannelIDs       []uuid.UUID `json:"channelIds"`
}

// BudgetDTO is a budget with this month's spend and where it is heading.
type BudgetDTO struct {
	Budget
	Spent     float64 `json:"spent"`
	Projected float64 `json:"projected"`
	Percent   float64 `json:"percent"`
	Status    string  `json:"status"`
	// Method: forecast (the provider's, for an unfiltered budget) or run-rate (the last days' average).
	Method string `json:"method"`
}

// AlertSettingsDTO is where a project's new cost anomalies are sent.
type AlertSettingsDTO struct {
	AnomalyChannelIDs []uuid.UUID `json:"anomalyChannelIds"`
}

func (s *Service) ListBudgets(ctx context.Context, projectID uuid.UUID) ([]BudgetDTO, error) {
	var budgets []Budget
	if err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Order("lower(name)").
		Find(&budgets).Error; err != nil {
		return nil, err
	}
	result := make([]BudgetDTO, 0, len(budgets))
	for _, budget := range budgets {
		dto, err := s.budgetStatus(ctx, budget, s.now())
		if err != nil {
			return nil, err
		}
		result = append(result, dto)
	}
	return result, nil
}

func (s *Service) CreateBudget(
	ctx context.Context, projectID uuid.UUID, userID uuid.UUID, request BudgetRequest,
) (*BudgetDTO, error) {
	budget, err := budgetFrom(request)
	if err != nil {
		return nil, err
	}
	budget.ID, budget.ProjectID, budget.CreatedBy = uuid.New(), projectID, &userID
	budget.CreatedAt, budget.UpdatedAt = s.now(), s.now()
	err = storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(budget).Error; err != nil {
			if strings.Contains(err.Error(), "SQLSTATE 23505") {
				return fmt.Errorf("a budget named %q already exists", budget.Name)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := alerting.SaveManagedRule(budgetRule(budget)); err != nil {
		_ = storage.GetDb().Delete(&Budget{}, "id = ?", budget.ID).Error
		return nil, err
	}
	dto, err := s.budgetStatus(ctx, *budget, s.now())
	return &dto, err
}

func (s *Service) UpdateBudget(
	ctx context.Context, projectID, id uuid.UUID, request BudgetRequest,
) (*BudgetDTO, error) {
	existing, err := s.findBudget(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	budget, err := budgetFrom(request)
	if err != nil {
		return nil, err
	}
	budget.ID, budget.ProjectID, budget.CreatedBy, budget.CreatedAt = id, projectID, existing.CreatedBy, existing.CreatedAt
	budget.UpdatedAt = s.now()
	err = storage.GetDb().WithContext(ctx).Model(&Budget{}).Where("id = ?", id).Select("*").
		Omit("id", "project_id", "created_by", "created_at").Updates(budget).Error
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23505") {
			return nil, fmt.Errorf("a budget named %q already exists", budget.Name)
		}
		return nil, err
	}
	if err := alerting.SaveManagedRule(budgetRule(budget)); err != nil {
		return nil, err
	}
	dto, err := s.budgetStatus(ctx, *budget, s.now())
	return &dto, err
}

func (s *Service) DeleteBudget(ctx context.Context, projectID, id uuid.UUID) error {
	if _, err := s.findBudget(ctx, projectID, id); err != nil {
		return err
	}
	if err := alerting.DeleteManagedRule(budgetRuleKey(id)); err != nil {
		return err
	}
	return storage.GetDb().WithContext(ctx).Delete(&Budget{}, "id = ?", id).Error
}

func (s *Service) GetAlertSettings(ctx context.Context, projectID uuid.UUID) (*AlertSettingsDTO, error) {
	var rows []alertSettings
	if err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &AlertSettingsDTO{AnomalyChannelIDs: []uuid.UUID{}}, nil
	}
	return &AlertSettingsDTO{AnomalyChannelIDs: append([]uuid.UUID{}, rows[0].AnomalyChannelIDs...)}, nil
}

// UpdateAlertSettings keeps the project's anomaly alert rule: none without channels.
func (s *Service) UpdateAlertSettings(
	ctx context.Context, projectID uuid.UUID, request AlertSettingsDTO,
) (*AlertSettingsDTO, error) {
	channels, err := alerting.UsableChannelIDs(projectID, request.AnomalyChannelIDs)
	if err != nil {
		return nil, err
	}
	row := alertSettings{ProjectID: projectID, AnomalyChannelIDs: channels, UpdatedAt: s.now()}
	err = storage.GetDb().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"anomaly_channel_ids", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	rules := []alerting.ManagedRule{
		{
			Key: anomalyRuleKey(projectID), ProjectID: projectID, Name: "Cloud cost anomaly",
			Source: alerting.SourceCloudCost, Query: alerting.RuleQuery{CostAnomalies: true}, Comparison: ">",
			Threshold: 0, IntervalSeconds: anomalyRuleInterval, Labels: alerting.Labels{"source": "cost_anomaly"},
			Summary: "Cloud cost on {{ $labels.day }} for {{ $labels.service }} ({{ $labels.account }}) " +
				"was far from the usual for that weekday",
			ChannelIDs: channels,
		},
		{
			Key:             commitmentRuleKey(projectID),
			ProjectID:       projectID,
			Name:            "Cloud commitment ending",
			Source:          alerting.SourceCloudCost,
			Query:           alerting.RuleQuery{CostCommitments: true},
			Comparison:      "<=",
			Threshold:       endingSoonDays,
			IntervalSeconds: commitmentRuleInterval,
			Labels:          alerting.Labels{"source": "cost_commitment"},
			Summary:         "{{ $labels.commitment }} ({{ $labels.connection }}) ends on {{ $labels.ends }}: renew or replace it",
			ChannelIDs:      channels,
		},
	}
	for i := range rules {
		if len(channels) == 0 {
			if err := alerting.DeleteManagedRule(rules[i].Key); err != nil {
				return nil, err
			}
		} else if err := alerting.SaveManagedRule(&rules[i]); err != nil {
			return nil, err
		}
	}
	return &AlertSettingsDTO{AnomalyChannelIDs: append([]uuid.UUID{}, channels...)}, nil
}

// EvaluateRule is the cloud_cost alert source: a budget's projected percent of its amount, or a
// series per new anomaly.
func (s *Service) EvaluateRule(
	ctx context.Context,
	rule *alerting.AlertRule,
	now time.Time,
) ([]alerting.SourceSample, error) {
	if rule.Query.CostCommitments {
		return commitmentSamples(ctx, rule.ProjectID, now)
	}
	if rule.Query.CostAnomalies {
		var anomalies []Anomaly
		err := storage.GetDb().WithContext(ctx).Where("project_id = ? AND status = 'open' AND detected_at > ?",
			rule.ProjectID, now.Add(-anomalyAlertLookback)).Order("day").Limit(200).Find(&anomalies).Error
		if err != nil {
			return nil, err
		}
		samples := make([]alerting.SourceSample, 0, len(anomalies))
		for _, anomaly := range anomalies {
			service := anomaly.Service
			if service == "" {
				service = "all services"
			}
			samples = append(samples, alerting.SourceSample{Value: 1, Labels: alerting.Labels{
				"day": dayText(anomaly.Day), "account": anomaly.SubAccount, "service": service,
				"currency": anomaly.Currency,
			}})
		}
		return samples, nil
	}
	id, err := uuid.Parse(rule.Query.BudgetID)
	if err != nil {
		return nil, fmt.Errorf("the rule names no cost budget: %w", err)
	}
	budget, err := s.findBudget(ctx, rule.ProjectID, id)
	if err != nil {
		return nil, err
	}
	status, err := s.budgetStatus(ctx, *budget, now)
	if err != nil {
		return nil, err
	}
	return []alerting.SourceSample{{
		Value:  math.Round(status.Percent*10) / 10,
		Labels: alerting.Labels{"budget": budget.Name, "month": utcDay(now).Format("2006-01")},
	}}, nil
}

// budgetStatus is this month's spend and projection: the provider's forecast for an unfiltered
// budget, else the last runRateDays' average for the days left.
func (s *Service) budgetStatus(ctx context.Context, budget Budget, now time.Time) (BudgetDTO, error) {
	today := utcDay(now)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)
	projects := []uuid.UUID{budget.ProjectID}
	db := storage.GetDb().WithContext(ctx)
	sum := func(from, to time.Time) (float64, error) {
		where, args := filterSQL(projects, from, to, budget.Filters, "", len(budget.Filters.Resource) > 0)
		var total float64
		err := db.Raw("SELECT COALESCE(SUM(effective), 0)::float8 FROM cloud_cost_daily WHERE "+where+
			" AND currency = ?", append(args, budget.Currency)...).Scan(&total).Error
		return total, err
	}
	dto := BudgetDTO{Budget: budget}
	spent, err := sum(monthStart, today)
	if err != nil {
		return dto, err
	}
	dto.Spent, dto.Projected = spent, spent
	if budget.Filters.empty() {
		var remaining *float64
		err := db.Raw(`SELECT SUM(remaining)::float8 FROM cloud_cost_forecasts WHERE project_id = ? AND month = ?
			AND currency = ? AND updated_at > ?`, budget.ProjectID, monthStart.Format(dayLayout), budget.Currency,
			now.Add(-72*time.Hour)).Scan(&remaining).Error
		if err != nil {
			return dto, err
		}
		if remaining != nil {
			dto.Projected, dto.Method = spent+*remaining, "forecast"
		}
	}
	if dto.Method == "" {
		dto.Method = "run-rate"
		recent, err := sum(today.AddDate(0, 0, -runRateDays), today.AddDate(0, 0, -1))
		if err != nil {
			return dto, err
		}
		left := int(monthEnd.Sub(today)/(24*time.Hour)) + 1
		dto.Projected = spent + recent/runRateDays*float64(left)
	}
	if budget.Amount > 0 {
		dto.Percent = dto.Projected / budget.Amount * 100
	}
	switch {
	case dto.Percent >= 100:
		dto.Status = BudgetOver
	case dto.Percent >= float64(budget.ThresholdPercent):
		dto.Status = BudgetNear
	default:
		dto.Status = BudgetOK
	}
	return dto, nil
}

func (s *Service) findBudget(ctx context.Context, projectID, id uuid.UUID) (*Budget, error) {
	var budget Budget
	err := storage.GetDb().WithContext(ctx).Where("id = ? AND project_id = ?", id, projectID).First(&budget).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &budget, err
}

func budgetFrom(request BudgetRequest) (*Budget, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 100 {
		return nil, errors.New("a budget's name is 1 to 100 characters")
	}
	currency := strings.ToUpper(strings.TrimSpace(request.Currency))
	if !currencyPattern.MatchString(currency) {
		return nil, errors.New("the currency is a three-letter code like USD")
	}
	if request.Amount <= 0 || request.Amount > 1e12 {
		return nil, errors.New("the amount must be more than 0")
	}
	threshold := request.ThresholdPercent
	if threshold == 0 {
		threshold = 80
	}
	if threshold < 1 || threshold > 100 {
		return nil, errors.New("alert at 1 to 100 percent of the budget")
	}
	if err := request.Filters.validate(); err != nil {
		return nil, err
	}
	return &Budget{
		Name: name, Filters: request.Filters, Currency: currency, Amount: request.Amount,
		ThresholdPercent: threshold, ChannelIDs: request.ChannelIDs,
	}, nil
}

func budgetRule(budget *Budget) *alerting.ManagedRule {
	return &alerting.ManagedRule{
		Key: budgetRuleKey(budget.ID), ProjectID: budget.ProjectID, Name: "Cost budget: " + budget.Name,
		Source: alerting.SourceCloudCost, Query: alerting.RuleQuery{BudgetID: budget.ID.String()},
		Comparison: ">=", Threshold: float64(budget.ThresholdPercent), IntervalSeconds: budgetRuleInterval,
		RepeatSeconds: budgetRuleRepeat, Labels: alerting.Labels{"source": "cost_budget"},
		Summary: fmt.Sprintf("%s is heading for {{ $value }}%% of its %s %.2f monthly budget", budget.Name,
			budget.Currency, budget.Amount),
		ChannelIDs: budget.ChannelIDs,
	}
}

func budgetRuleKey(id uuid.UUID) string {
	return alerting.ManagedByCloudCostPrefix + "budget:" + id.String()
}

func commitmentRuleKey(projectID uuid.UUID) string {
	return alerting.ManagedByCloudCostPrefix + "commitments:" + projectID.String()
}

func anomalyRuleKey(projectID uuid.UUID) string {
	return alerting.ManagedByCloudCostPrefix + "anomalies:" + projectID.String()
}

func (f Filters) empty() bool {
	if len(f.Provider)+len(f.Account)+len(f.Region)+len(f.Service)+len(f.Resource)+len(f.Charge) > 0 {
		return false
	}
	for _, values := range f.Tags {
		if len(values) > 0 {
			return false
		}
	}
	return true
}

func (f Filters) validate() error {
	lists := [][]string{f.Provider, f.Account, f.Region, f.Service, f.Resource, f.Charge}
	for _, list := range lists {
		if len(list) > 200 {
			return errors.New("a filter takes at most 200 values")
		}
	}
	if len(f.Tags) > 20 {
		return errors.New("filter on at most 20 tags")
	}
	for key := range f.Tags {
		if !tagKeyPattern.MatchString(key) {
			return fmt.Errorf("%q isn't a tag key", key)
		}
	}
	return nil
}
