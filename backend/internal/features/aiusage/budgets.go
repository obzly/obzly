package aiusage

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	// Budget periods use IANA zones; the image may have no zoneinfo.
	_ "time/tzdata"

	"obzly/internal/cache"
	"obzly/internal/features/alerting"
	"obzly/internal/query"
	"obzly/internal/storage"
	"obzly/internal/util/leaderlock"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// Budget periods and scopes.
const (
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"

	ScopeProject   = "project"
	ScopeEachUser  = "each_user"
	ScopeEachModel = "each_model"
	ScopeUser      = "user"
	ScopeModel     = "model"
)

const (
	budgetEvalSeconds  = 300
	budgetSyncInterval = 5 * time.Minute
	maxBudgets         = 100
	maxThresholds      = 5
	topGroups          = 5
	// forecastMinElapsed keeps the first minutes of a period from
	// extrapolating one request into a huge forecast.
	forecastMinElapsed = time.Hour
)

var defaultThresholds = []float64{80, 100}

// Budget is a spending limit for a project's AI usage over a calendar
// period; it alerts through a managed rule and never stops spending.
type Budget struct {
	ID         uuid.UUID `json:"id"         gorm:"column:id;primaryKey"`
	ProjectID  uuid.UUID `json:"projectId"  gorm:"column:project_id"`
	Name       string    `json:"name"       gorm:"column:name"`
	AmountUSD  float64   `json:"amountUsd"  gorm:"column:amount_usd"`
	Period     string    `json:"period"     gorm:"column:period"`
	TimeZone   string    `json:"timeZone"   gorm:"column:time_zone"`
	Scope      string    `json:"scope"      gorm:"column:scope"`
	ScopeValue string    `json:"scopeValue" gorm:"column:scope_value"`
	// Thresholds are percents of AmountUSD, ascending.
	Thresholds        Thresholds `json:"thresholds"        gorm:"column:thresholds"`
	ChannelIDs        ChannelIDs `json:"channelIds"        gorm:"column:channel_ids"`
	IncludeClaudeCode bool       `json:"includeClaudeCode" gorm:"column:include_claude_code"`
	IncludeLLMApps    bool       `json:"includeLlmApps"    gorm:"column:include_llm_apps"`
	CreatedBy         *uuid.UUID `json:"createdBy"         gorm:"column:created_by"`
	CreatedAt         time.Time  `json:"createdAt"         gorm:"column:created_at"`
	UpdatedAt         time.Time  `json:"updatedAt"         gorm:"column:updated_at"`
}

func (Budget) TableName() string { return "ai_budgets" }

type Thresholds []float64

func (t Thresholds) Value() (driver.Value, error) {
	if t == nil {
		t = Thresholds{}
	}
	return jsonValue(t)
}

func (t *Thresholds) Scan(value any) error { return scanJSON(value, t) }

type ChannelIDs []uuid.UUID

func (c ChannelIDs) Value() (driver.Value, error) {
	if c == nil {
		c = ChannelIDs{}
	}
	return jsonValue(c)
}

func (c *ChannelIDs) Scan(value any) error { return scanJSON(value, c) }

type BudgetRequest struct {
	Name       string  `json:"name"`
	AmountUSD  float64 `json:"amountUsd"`
	Period     string  `json:"period"`
	TimeZone   string  `json:"timeZone"`
	Scope      string  `json:"scope"`
	ScopeValue string  `json:"scopeValue"`
	// Thresholds default to 80 and 100.
	Thresholds []float64   `json:"thresholds"`
	ChannelIDs []uuid.UUID `json:"channelIds"`
	// IncludeClaudeCode defaults to true.
	IncludeClaudeCode *bool `json:"includeClaudeCode"`
	IncludeLLMApps    bool  `json:"includeLlmApps"`
}

type BudgetGroupDTO struct {
	Key      string  `json:"key"`
	SpentUSD float64 `json:"spentUsd"`
	// Percent of the budget; for project and one-user budgets the top list
	// is by model, as a share of the budget too.
	Percent float64 `json:"percent"`
}

type BudgetStatusDTO struct {
	BudgetID uuid.UUID `json:"budgetId"`
	// Current period, Unix ms; it resets at PeriodEnd.
	PeriodStart int64 `json:"periodStart"`
	PeriodEnd   int64 `json:"periodEnd"`
	// SpentUSD is the scope's total (every user or model for each_*).
	SpentUSD float64 `json:"spentUsd"`
	// Percent and ForecastUSD are the budget's, or for each user/model
	// budgets the highest-spending one's.
	Percent     float64 `json:"percent"`
	ForecastUSD float64 `json:"forecastUsd"`
	// Groups: users or models with spend (each_* only), and how many are
	// past the first threshold (below 100%) or at 100% and over.
	Groups        int              `json:"groups"`
	GroupsWarning int              `json:"groupsWarning"`
	GroupsOver    int              `json:"groupsOver"`
	Top           []BudgetGroupDTO `json:"top"`
	Error         string           `json:"error,omitempty"`
}

// budgetSpend is spend this period, by group key ("" when the scope has
// none) and by model.
type budgetSpend struct {
	byKey   map[string]float64
	byModel map[string]float64
}

func (s *AIUsageService) ListBudgets(projectID uuid.UUID) ([]*Budget, error) {
	var budgets []*Budget
	err := storage.GetDb().Where("project_id = ?", projectID).Order("lower(name), created_at").Find(&budgets).Error
	return budgets, err
}

func (s *AIUsageService) CreateBudget(projectID uuid.UUID, request BudgetRequest, userID uuid.UUID) (*Budget, error) {
	var count int64
	if err := storage.GetDb().Model(&Budget{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count >= maxBudgets {
		return nil, fmt.Errorf("%w: at most %d budgets per project", ErrInvalidRequest, maxBudgets)
	}
	now := time.Now().UTC()
	budget := &Budget{ID: uuid.New(), ProjectID: projectID, CreatedBy: &userID, CreatedAt: now, UpdatedAt: now}
	if err := applyBudget(budget, request); err != nil {
		return nil, err
	}
	if err := storage.GetDb().Create(budget).Error; err != nil {
		return nil, fmt.Errorf("failed to save budget: %w", err)
	}
	if err := alerting.SaveManagedRule(budgetRule(budget)); err != nil {
		storage.GetDb().Delete(&Budget{}, "id = ?", budget.ID)
		return nil, fmt.Errorf("failed to save the budget's alert rule: %w", err)
	}
	s.audit.WriteAuditLog("AI budget created: "+budget.Name, &userID, &projectID)
	return budget, nil
}

func (s *AIUsageService) UpdateBudget(
	projectID, budgetID uuid.UUID, request BudgetRequest, userID uuid.UUID,
) (*Budget, error) {
	budget, err := loadBudget(projectID, budgetID)
	if err != nil {
		return nil, err
	}
	if err := applyBudget(budget, request); err != nil {
		return nil, err
	}
	budget.UpdatedAt = time.Now().UTC()
	if err := storage.GetDb().Save(budget).Error; err != nil {
		return nil, fmt.Errorf("failed to save budget: %w", err)
	}
	if err := alerting.SaveManagedRule(budgetRule(budget)); err != nil {
		return nil, fmt.Errorf("failed to save the budget's alert rule: %w", err)
	}
	s.audit.WriteAuditLog("AI budget updated: "+budget.Name, &userID, &projectID)
	return budget, nil
}

func (s *AIUsageService) DeleteBudget(projectID, budgetID uuid.UUID, userID uuid.UUID) error {
	budget, err := loadBudget(projectID, budgetID)
	if err != nil {
		return err
	}
	if err := alerting.DeleteManagedRule(budgetRuleKey(budget.ID)); err != nil {
		return fmt.Errorf("failed to delete the budget's alert rule: %w", err)
	}
	if err := storage.GetDb().Delete(&Budget{}, "id = ?", budget.ID).Error; err != nil {
		return err
	}
	s.audit.WriteAuditLog("AI budget deleted: "+budget.Name, &userID, &projectID)
	return nil
}

// BudgetStatuses is each budget's spend this period; a budget whose spend
// couldn't be read carries the error instead.
func (s *AIUsageService) BudgetStatuses(ctx context.Context, projectID uuid.UUID) ([]BudgetStatusDTO, error) {
	budgets, err := s.ListBudgets(projectID)
	if err != nil {
		return nil, err
	}
	prices, err := loadPrices(projectID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	statuses := make([]BudgetStatusDTO, 0, len(budgets))
	for _, budget := range budgets {
		start, end := periodBounds(budget.Period, budget.location(), now)
		status := BudgetStatusDTO{
			BudgetID: budget.ID, PeriodStart: start.UnixMilli(), PeriodEnd: end.UnixMilli(), Top: []BudgetGroupDTO{},
		}
		spend, err := s.periodSpend(ctx, budget, prices, start, now)
		if refusal, ok := query.BudgetRefusal(err); ok {
			status.Error = "Couldn't read this period's spend: " + refusal
		} else if err != nil {
			status.Error = "Couldn't read this period's spend"
		} else {
			fillStatus(&status, budget, spend, start, end, now)
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// StartBudgetRuleSync re-creates missing budget rules now and every
// budgetSyncInterval: a replica still on an older version removes every
// managed rule it doesn't know during a rolling upgrade.
func (s *AIUsageService) StartBudgetRuleSync(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(budgetSyncInterval)
		defer ticker.Stop()
		for {
			_, err := leaderlock.RunExclusive(ctx, cache.GetCache(), "ai-budget-rule-sync", time.Minute,
				func(context.Context) error { return s.SyncBudgetRules() })
			if err != nil {
				logger.GetLogger().Error("AI budget rule sync failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// SyncBudgetRules creates the managed rule of every budget that has none.
func (s *AIUsageService) SyncBudgetRules() error {
	var budgets []*Budget
	if err := storage.GetDb().Find(&budgets).Error; err != nil {
		return err
	}
	keys, err := alerting.ManagedRuleKeys(alerting.ManagedByBudgetPrefix)
	if err != nil {
		return err
	}
	for _, budget := range budgets {
		if keys[budgetRuleKey(budget.ID)] {
			continue
		}
		if err := alerting.SaveManagedRule(budgetRule(budget)); err != nil {
			return fmt.Errorf("budget %s: %w", budget.ID, err)
		}
	}
	return nil
}

// EvaluateBudgetRule is the ai_cost alert source: a series per user or
// model and threshold crossed this period. The period is a label, so an
// alert fires once per period and resolves when the period rolls over.
func (s *AIUsageService) EvaluateBudgetRule(
	ctx context.Context, rule *alerting.AlertRule, now time.Time,
) ([]alerting.SourceSample, error) {
	budgetID, err := uuid.Parse(rule.Query.BudgetID)
	if err != nil {
		return nil, errors.New("the rule names no budget")
	}
	budget, err := loadBudget(rule.ProjectID, budgetID)
	if err != nil {
		return nil, fmt.Errorf("budget %s: %w", budgetID, err)
	}
	prices, err := loadPrices(budget.ProjectID)
	if err != nil {
		return nil, err
	}
	start, _ := periodBounds(budget.Period, budget.location(), now)
	spend, err := s.periodSpend(ctx, budget, prices, start, now)
	if err != nil {
		return nil, err
	}
	return budgetSamples(budget, spend, start), nil
}

// periodBounds is the calendar day, ISO week (from Monday) or month
// containing now, in loc.
func periodBounds(period string, loc *time.Location, now time.Time) (time.Time, time.Time) {
	local := now.In(loc)
	year, month, day := local.Date()
	var start, end time.Time
	switch period {
	case PeriodWeek:
		offset := (int(local.Weekday()) + 6) % 7
		start = time.Date(year, month, day-offset, 0, 0, 0, 0, loc)
		end = time.Date(year, month, day-offset+7, 0, 0, 0, 0, loc)
	case PeriodMonth:
		start = time.Date(year, month, 1, 0, 0, 0, 0, loc)
		end = time.Date(year, month+1, 1, 0, 0, 0, 0, loc)
	default:
		start = time.Date(year, month, day, 0, 0, 0, 0, loc)
		end = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
	}
	return start.UTC(), end.UTC()
}

// forecast is spent extrapolated to the period's end at the rate so far.
func forecast(spent float64, start, end, now time.Time) float64 {
	if !now.Before(end) {
		return spent
	}
	elapsed := max(now.Sub(start), min(forecastMinElapsed, end.Sub(start)))
	return spent * float64(end.Sub(start)) / float64(elapsed)
}

func budgetSamples(budget *Budget, spend *budgetSpend, periodStart time.Time) []alerting.SourceSample {
	period := periodStart.In(budget.location()).Format("2006-01-02")
	keys := make([]string, 0, len(spend.byKey))
	for key := range spend.byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	samples := []alerting.SourceSample{}
	for _, key := range keys {
		percent := spend.byKey[key] / budget.AmountUSD * 100
		for _, threshold := range budget.Thresholds {
			if percent < threshold {
				continue
			}
			labels := alerting.Labels{
				"period": period, "threshold": formatPercent(threshold), "severity": severity(threshold),
			}
			if name, value := budget.groupLabel(key); name != "" {
				labels[name] = value
			}
			samples = append(samples, alerting.SourceSample{Labels: labels, Value: math.Round(percent*10) / 10})
		}
	}
	return samples
}

func fillStatus(status *BudgetStatusDTO, budget *Budget, spend *budgetSpend, start, end, now time.Time) {
	top := []BudgetGroupDTO{}
	for key, spent := range spend.byKey {
		status.SpentUSD += spent
		if budget.perGroup() {
			top = append(top, BudgetGroupDTO{Key: key, SpentUSD: spent, Percent: spent / budget.AmountUSD * 100})
		}
	}
	if !budget.perGroup() && budget.Scope != ScopeModel {
		for model, spent := range spend.byModel {
			top = append(top, BudgetGroupDTO{Key: model, SpentUSD: spent, Percent: spent / budget.AmountUSD * 100})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].SpentUSD != top[j].SpentUSD {
			return top[i].SpentUSD > top[j].SpentUSD
		}
		return top[i].Key < top[j].Key
	})

	measured := status.SpentUSD
	if budget.perGroup() {
		status.Groups = len(top)
		measured = 0
		if len(top) > 0 {
			measured = top[0].SpentUSD
		}
		first := 100.0
		if len(budget.Thresholds) > 0 {
			first = budget.Thresholds[0]
		}
		for _, group := range top {
			switch {
			case group.Percent >= 100:
				status.GroupsOver++
			case group.Percent >= first:
				status.GroupsWarning++
			}
		}
	}
	status.Percent = measured / budget.AmountUSD * 100
	status.ForecastUSD = forecast(measured, start, end, now)
	if len(top) > topGroups {
		top = top[:topGroups]
	}
	status.Top = top
}

func (b *Budget) perGroup() bool {
	return b.Scope == ScopeEachUser || b.Scope == ScopeEachModel
}

func (b *Budget) location() *time.Location {
	loc, err := time.LoadLocation(b.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// groupLabel is the alert label naming a group's user or model.
func (b *Budget) groupLabel(key string) (string, string) {
	switch b.Scope {
	case ScopeEachUser:
		return "user", key
	case ScopeEachModel:
		return "model", key
	case ScopeUser:
		return "user", b.ScopeValue
	case ScopeModel:
		return "model", b.ScopeValue
	}
	return "", ""
}

func applyBudget(budget *Budget, request BudgetRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	request.ScopeValue = strings.TrimSpace(request.ScopeValue)
	if request.TimeZone == "" {
		request.TimeZone = "UTC"
	}
	if request.Thresholds == nil {
		request.Thresholds = defaultThresholds
	}
	includeClaudeCode := request.IncludeClaudeCode == nil || *request.IncludeClaudeCode
	switch {
	case request.Name == "" || len(request.Name) > 200:
		return fmt.Errorf("%w: a name of up to 200 characters is required", ErrInvalidRequest)
	case math.IsNaN(request.AmountUSD) || request.AmountUSD <= 0 || request.AmountUSD > 1e9:
		return fmt.Errorf("%w: the amount must be more than 0 USD", ErrInvalidRequest)
	case !slices.Contains([]string{PeriodDay, PeriodWeek, PeriodMonth}, request.Period):
		return fmt.Errorf("%w: period must be day, week or month", ErrInvalidRequest)
	case !slices.Contains([]string{ScopeProject, ScopeEachUser, ScopeEachModel, ScopeUser, ScopeModel}, request.Scope):
		return fmt.Errorf("%w: scope must be project, each_user, each_model, user or model", ErrInvalidRequest)
	case (request.Scope == ScopeUser || request.Scope == ScopeModel) &&
		(request.ScopeValue == "" || len(request.ScopeValue) > 500):
		return fmt.Errorf("%w: name the user or model the budget is for", ErrInvalidRequest)
	case !includeClaudeCode && !request.IncludeLLMApps:
		return fmt.Errorf("%w: count Claude Code, LLM apps or both", ErrInvalidRequest)
	case len(request.Thresholds) == 0 || len(request.Thresholds) > maxThresholds:
		return fmt.Errorf("%w: between 1 and %d thresholds", ErrInvalidRequest, maxThresholds)
	case len(request.ChannelIDs) > 20:
		return fmt.Errorf("%w: at most 20 channels", ErrInvalidRequest)
	}
	if request.TimeZone == "Local" {
		return fmt.Errorf("%w: use an IANA time zone such as Europe/Berlin", ErrInvalidRequest)
	}
	if _, err := time.LoadLocation(request.TimeZone); err != nil {
		return fmt.Errorf("%w: unknown time zone %q", ErrInvalidRequest, request.TimeZone)
	}
	thresholds := slices.Clone(request.Thresholds)
	slices.Sort(thresholds)
	thresholds = slices.Compact(thresholds)
	for _, threshold := range thresholds {
		if math.IsNaN(threshold) || threshold <= 0 || threshold > 1000 {
			return fmt.Errorf("%w: thresholds are percents between 0 and 1000", ErrInvalidRequest)
		}
	}
	channels, err := alerting.UsableChannelIDs(budget.ProjectID, request.ChannelIDs)
	if err != nil {
		return err
	}
	if len(channels) != len(uniqueIDs(request.ChannelIDs)) {
		return fmt.Errorf("%w: every channel must be available to this project", ErrInvalidRequest)
	}
	if request.Scope != ScopeUser && request.Scope != ScopeModel {
		request.ScopeValue = ""
	}

	budget.Name, budget.AmountUSD, budget.Period, budget.TimeZone = request.Name, request.AmountUSD, request.Period, request.TimeZone
	budget.Scope, budget.ScopeValue, budget.Thresholds = request.Scope, request.ScopeValue, thresholds
	budget.ChannelIDs, budget.IncludeClaudeCode, budget.IncludeLLMApps = channels, includeClaudeCode, request.IncludeLLMApps
	return nil
}

func budgetRule(budget *Budget) *alerting.ManagedRule {
	scope := map[string]string{
		ScopeProject: "the whole project", ScopeEachUser: "each user", ScopeEachModel: "each model",
		ScopeUser: "user " + budget.ScopeValue, ScopeModel: "model " + budget.ScopeValue,
	}[budget.Scope]
	return &alerting.ManagedRule{
		Key:       budgetRuleKey(budget.ID),
		ProjectID: budget.ProjectID,
		Name:      "AI budget: " + budget.Name,
		Source:    alerting.SourceAICost,
		Query:     alerting.RuleQuery{BudgetID: budget.ID.String()},
		// The value is the percent spent; each series is one threshold crossed.
		Comparison:      ">=",
		Threshold:       budget.Thresholds[0],
		IntervalSeconds: budgetEvalSeconds,
		RepeatSeconds:   0,
		Labels:          alerting.Labels{"source": "ai_budget"},
		Summary: fmt.Sprintf("%s per %s for %s (%s). Spend past a threshold notifies once per period; "+
			"budgets alert, they can't stop spending.", formatUSD(budget.AmountUSD), budget.Period, scope, budget.TimeZone),
		ChannelIDs: budget.ChannelIDs,
	}
}

func budgetRuleKey(id uuid.UUID) string {
	return alerting.ManagedByBudgetPrefix + id.String()
}

func loadBudget(projectID, budgetID uuid.UUID) (*Budget, error) {
	var budget Budget
	if err := storage.GetDb().Where("project_id = ? AND id = ?", projectID, budgetID).First(&budget).Error; err != nil {
		return nil, notFound(err)
	}
	return &budget, nil
}

func severity(threshold float64) string {
	if threshold >= 100 {
		return "critical"
	}
	return "warning"
}

func formatPercent(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func formatUSD(value float64) string {
	return "$" + strconv.FormatFloat(value, 'f', 2, 64)
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	return unique
}

func jsonValue(v any) (driver.Value, error) {
	encoded, err := json.Marshal(v)
	return string(encoded), err
}

func scanJSON(value any, target any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(v, target)
	case string:
		return json.Unmarshal([]byte(v), target)
	default:
		return fmt.Errorf("unexpected JSON column type %T", value)
	}
}
