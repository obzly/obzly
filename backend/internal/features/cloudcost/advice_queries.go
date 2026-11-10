package cloudcost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/promql"

	"obzly/internal/features/alerting"
	"obzly/internal/features/audit_logs"
	metrics_promql "obzly/internal/features/metrics/promql"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

const (
	daysPerMonth = 30.4375
	// endingSoonDays: a commitment ending within this many days is flagged and alerts.
	endingSoonDays = 60
	// unusedBelow: a commitment used less than this (percent) is flagged as unused.
	unusedBelow = 1.0
	// doneShownFor: done recommendations whose resource is gone stay listed this long.
	doneShownFor = 180 * 24 * time.Hour
	// agentWindow: the Obzly agent's CPU and memory p95 over the last 14 days.
	agentWindow = 14 * 24 * time.Hour
	agentStep   = time.Hour
	// agentBusyMemory: memory this busy (p95, percent) makes a smaller size risky.
	agentBusyMemory = 80
	maxAgentHosts   = 200
)

// RecommendationDTO is a recommendation with what people decided about it. MonthlySavings is
// nil when it can't be worked out (no resource-level cost yet): unknown, never zero.
type RecommendationDTO struct {
	Key             string            `json:"key"`
	ProjectID       uuid.UUID         `json:"projectId"`
	ConnectionID    *uuid.UUID        `json:"connectionId,omitempty"`
	ConnectionName  string            `json:"connectionName"`
	Provider        string            `json:"provider"`
	Source          string            `json:"source"`
	Kind            string            `json:"kind"`
	Account         string            `json:"account"`
	Region          string            `json:"region"`
	ResourceKey     string            `json:"resourceKey"`
	ResourceName    string            `json:"resourceName"`
	ResourceType    string            `json:"resourceType"`
	Action          string            `json:"action"`
	Current         string            `json:"current,omitempty"`
	Target          string            `json:"target,omitempty"`
	Currency        string            `json:"currency"`
	MonthlySavings  *float64          `json:"monthlySavings"`
	SavingsBasis    string            `json:"savingsBasis"`
	Effort          string            `json:"effort"`
	Risk            string            `json:"risk"`
	Evidence        map[string]string `json:"evidence"`
	RefreshedAt     *time.Time        `json:"refreshedAt,omitempty"`
	Status          string            `json:"status"`
	Owner           string            `json:"owner,omitempty"`
	DueDate         string            `json:"dueDate,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	DoneAt          *time.Time        `json:"doneAt,omitempty"`
	DoneBy          string            `json:"doneBy,omitempty"`
	ExpectedMonthly *float64          `json:"expectedMonthly,omitempty"`
	RealizedMonthly *float64          `json:"realizedMonthly,omitempty"`
	MeasuredAt      *time.Time        `json:"measuredAt,omitempty"`
}

// AdviceSectionDTO is how a connection's last advice refresh went for one section.
type AdviceSectionDTO struct {
	ProjectID      uuid.UUID `json:"projectId"`
	ConnectionID   uuid.UUID `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	Section        string    `json:"section"`
	State          string    `json:"state"`
	Reason         string    `json:"reason,omitempty"`
	RefreshedAt    time.Time `json:"refreshedAt"`
}

// RecommendationsDTO is the Recommendations tab.
type RecommendationsDTO struct {
	Items    []RecommendationDTO `json:"items"`
	Sections []AdviceSectionDTO  `json:"sections"`
}

// RecommendationUpdate changes a recommendation's status: planned (owner, due date), done,
// dismissed (with a reason) or open again.
type RecommendationUpdate struct {
	ProjectID uuid.UUID `json:"projectId"         binding:"required"`
	Key       string    `json:"key"               binding:"required"`
	Status    string    `json:"status"            binding:"required"`
	Owner     string    `json:"owner,omitempty"`
	DueDate   string    `json:"dueDate,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// CommitmentDTO is an existing commitment with what it's flagged for.
type CommitmentDTO struct {
	ProjectID      uuid.UUID  `json:"projectId"`
	ConnectionID   uuid.UUID  `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	Provider       string     `json:"provider"`
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Account        string     `json:"account,omitempty"`
	Region         string     `json:"region,omitempty"`
	Description    string     `json:"description"`
	Term           string     `json:"term,omitempty"`
	Payment        string     `json:"payment,omitempty"`
	StartsAt       *time.Time `json:"startsAt,omitempty"`
	EndsAt         *time.Time `json:"endsAt,omitempty"`
	EndsInDays     *int       `json:"endsInDays,omitempty"`
	Currency       string     `json:"currency,omitempty"`
	MonthlyCost    *float64   `json:"monthlyCost"`
	Utilization    *float64   `json:"utilization"`
	UnusedMonthly  *float64   `json:"unusedMonthly"`
	Unused         bool       `json:"unused"`
	EndingSoon     bool       `json:"endingSoon"`
}

// CoverageDTO is how much eligible usage commitments of a type covered over 30 days.
type CoverageDTO struct {
	ConnectionID    uuid.UUID `json:"connectionId"`
	ConnectionName  string    `json:"connectionName"`
	Type            string    `json:"type"`
	Currency        string    `json:"currency,omitempty"`
	CoveredPercent  float64   `json:"coveredPercent"`
	OnDemandMonthly *float64  `json:"onDemandMonthly"`
}

// CommitmentsDTO is the Commitments view.
type CommitmentsDTO struct {
	Commitments []CommitmentDTO    `json:"commitments"`
	Coverage    []CoverageDTO      `json:"coverage"`
	Sections    []AdviceSectionDTO `json:"sections"`
}

// PurchaseOptionDTO is one term and payment of a plan, already reduced for rightsizing first
// when asked.
type PurchaseOptionDTO struct {
	Term               string            `json:"term"`
	Payment            string            `json:"payment"`
	Offered            bool              `json:"offered"`
	Reason             string            `json:"reason,omitempty"`
	Commitment         string            `json:"commitment,omitempty"`
	HourlyCommitment   float64           `json:"hourlyCommitment"`
	Upfront            float64           `json:"upfront"`
	MonthlyRecurring   float64           `json:"monthlyRecurring"`
	MonthlySavings     float64           `json:"monthlySavings"`
	SavingsPercent     float64           `json:"savingsPercent"`
	BreakEvenMonths    float64           `json:"breakEvenMonths"`
	AverageUtilization float64           `json:"averageUtilization"`
	CoveragePercent    float64           `json:"coveragePercent"`
	Lines              []PurchaseLineDTO `json:"lines"`
	Labels             []string          `json:"labels"`
}

// PlanDTO is what the planner compares for one connection, type and look-back.
type PlanDTO struct {
	ConnectionID    uuid.UUID `json:"connectionId"`
	ConnectionName  string    `json:"connectionName"`
	Provider        string    `json:"provider"`
	Type            string    `json:"type"`
	LookbackDays    int       `json:"lookbackDays"`
	Currency        string    `json:"currency"`
	OnDemandMonthly float64   `json:"onDemandMonthly"`
	// RightsizingMonthly: open rightsizing and idle savings on the usage this type covers;
	// Factor is what the plan was scaled by so the two never count the same saving.
	RightsizingMonthly float64             `json:"rightsizingMonthly"`
	Factor             float64             `json:"factor"`
	RefreshedAt        time.Time           `json:"refreshedAt"`
	Options            []PurchaseOptionDTO `json:"options"`
	Usage              []UsagePoint        `json:"usage"`
}

// UsagePoint is one day of the usage a type covers: on demand, and already committed.
type UsagePoint struct {
	Day       string  `json:"day"`
	OnDemand  float64 `json:"onDemand"`
	Committed float64 `json:"committed"`
}

// PlannerDTO is the planner.
type PlannerDTO struct {
	Plans    []PlanDTO          `json:"plans"`
	Sections []AdviceSectionDTO `json:"sections"`
}

// typeUsage: the services (FOCUS ServiceName patterns) and resource types a commitment type
// covers, for the usage chart and rightsizing first.
var typeUsage = map[string]struct {
	services  []string
	resources []string
}{
	// FOCUS names services ("Amazon Elastic Compute Cloud"); some exports carry the product code.
	spec.CommitComputeSP: {[]string{"%Elastic Compute Cloud%", "AmazonEC2%", "%Lambda%", "%Elastic Container Service%",
		"AmazonECS%"},
		[]string{"aws_ec2_instance", "aws_autoscaling_group", "aws_ecs_service"}},
	spec.CommitEC2SP: {
		[]string{"%Elastic Compute Cloud%", "AmazonEC2%"},
		[]string{"aws_ec2_instance", "aws_autoscaling_group"},
	},
	spec.CommitEC2RI: {
		[]string{"%Elastic Compute Cloud%", "AmazonEC2%"},
		[]string{"aws_ec2_instance", "aws_autoscaling_group"},
	},
	spec.CommitRDSRI: {[]string{"%Relational Database%", "AmazonRDS%"}, []string{"aws_rds_instance"}},
	spec.CommitAzureSP: {
		[]string{"Virtual Machines%", "%App Service%", "%Container Instances%"},
		[]string{"azure_virtual_machine"},
	},
	spec.CommitAzureReserve: {[]string{"Virtual Machines%"}, []string{"azure_virtual_machine"}},
}

// paymentRisk orders payments from least to most paid in advance.
var paymentRisk = map[string]int{spec.PayNoUpfront: 0, spec.PayMonthly: 0, spec.PayPartial: 1,
	spec.PayAllUpfront: 2, spec.PayUpfront: 2}

// Recommendations lists the projects' recommendations, plus done ones whose resource is gone.
func (s *Service) Recommendations(ctx context.Context, projects []uuid.UUID) (*RecommendationsDTO, error) {
	db := storage.GetDb().WithContext(ctx)
	var rows []recommendationRow
	if err := db.Where("project_id IN ?", projects).Order("key").Find(&rows).Error; err != nil {
		return nil, err
	}
	var states []recommendationState
	if err := db.Where("project_id IN ?", projects).Find(&states).Error; err != nil {
		return nil, err
	}
	byKey := map[string]recommendationState{}
	for _, state := range states {
		byKey[state.ProjectID.String()+"|"+state.Key] = state
	}
	items := make([]RecommendationDTO, 0, len(rows))
	listed := map[string]bool{}
	for _, row := range rows {
		id := row.ProjectID.String() + "|" + row.Key
		listed[id] = true
		connection := row.ConnectionID
		item := RecommendationDTO{
			Key: row.Key, ProjectID: row.ProjectID, ConnectionID: &connection, ConnectionName: row.ConnectionName,
			Provider: row.Provider, Source: row.Source, Kind: row.Kind, Account: row.Account, Region: row.Region,
			ResourceKey: row.ResourceKey, ResourceName: row.ResourceName, ResourceType: row.ResourceType,
			Action: row.Action, Current: row.Current, Target: row.Target, Currency: row.Currency,
			MonthlySavings: parseAmount(row.MonthlySavings), SavingsBasis: savingsBasis(row), Effort: row.Effort,
			Risk: row.Risk, Evidence: row.Evidence, RefreshedAt: &row.RefreshedAt, Status: statusOpen,
		}
		if state, ok := byKey[id]; ok {
			applyState(&item, state)
		}
		items = append(items, item)
	}
	cutoff := s.now().Add(-doneShownFor)
	for id, state := range byKey {
		if listed[id] || state.Status != statusDone || state.DoneAt == nil || state.DoneAt.Before(cutoff) {
			continue
		}
		item := RecommendationDTO{Key: state.Key, ProjectID: state.ProjectID, ResourceKey: state.ResourceKey,
			ResourceName: state.ResourceName, Action: state.Action, Currency: state.Currency,
			Evidence: map[string]string{}, Provider: resourceProvider(state.ResourceKey)}
		applyState(&item, state)
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return amountOr(items[i].MonthlySavings) > amountOr(items[j].MonthlySavings)
	})
	sections, err := adviceSections(ctx, projects)
	if err != nil {
		return nil, err
	}
	return &RecommendationsDTO{Items: items, Sections: sections}, nil
}

// UpdateRecommendation records a decision about a recommendation; plans and dismissals go into
// the audit log.
func (s *Service) UpdateRecommendation(
	ctx context.Context, user *users_models.User, update RecommendationUpdate,
) (*RecommendationDTO, error) {
	if !slices.Contains([]string{statusOpen, statusPlanned, statusDone, statusDismissed}, update.Status) {
		return nil, errors.New("status must be open, planned, done or dismissed")
	}
	if update.Status == statusDismissed && strings.TrimSpace(update.Reason) == "" {
		return nil, errors.New("say why it's dismissed")
	}
	if len(update.Owner) > 200 || len(update.Reason) > maxAdviceText {
		return nil, errors.New("the owner or reason is too long")
	}
	var due *time.Time
	if update.DueDate != "" {
		day, err := parseDay(update.DueDate)
		if err != nil {
			return nil, err
		}
		due = &day
	}
	db := storage.GetDb().WithContext(ctx)
	var row recommendationRow
	found := db.Where("project_id = ? AND key = ?", update.ProjectID, update.Key).Limit(1).Find(&row).RowsAffected > 0
	var existing recommendationState
	hasState := db.Where("project_id = ? AND key = ?", update.ProjectID, update.Key).Limit(1).Find(&existing).
		RowsAffected > 0
	if !found && !hasState {
		return nil, ErrNotFound
	}
	now := s.now()
	state := existing
	state.ProjectID, state.Key, state.Status, state.UpdatedBy, state.UpdatedAt = update.ProjectID, update.Key,
		update.Status, user.Email, now
	state.Owner, state.DueDate, state.Reason = strings.TrimSpace(update.Owner), due, strings.TrimSpace(update.Reason)
	if found {
		state.Action, state.ResourceKey, state.ResourceName, state.Currency = row.Action, row.ResourceKey,
			row.ResourceName, row.Currency
		state.ExpectedMonthly = parseAmount(row.MonthlySavings)
	}
	if update.Status == statusDone {
		if state.DoneAt == nil || existing.Status != statusDone {
			state.DoneAt, state.DoneBy, state.RealizedMonthly, state.MeasuredAt = &now, user.Email, nil, nil
		}
	} else {
		state.DoneAt, state.DoneBy, state.RealizedMonthly, state.MeasuredAt = nil, "", nil, nil
	}
	if err := db.Save(&state).Error; err != nil {
		return nil, err
	}
	what := map[string]string{statusOpen: "Reopened", statusPlanned: "Planned", statusDone: "Marked done",
		statusDismissed: "Dismissed"}[update.Status]
	message := fmt.Sprintf("%s cost recommendation %q on %s", what, state.Action, state.ResourceKey)
	if update.Status == statusDismissed {
		message += ": " + state.Reason
	}
	audit_logs.GetAuditLogService().WriteAuditLog(message, &user.ID, &update.ProjectID)
	result, err := s.Recommendations(ctx, []uuid.UUID{update.ProjectID})
	if err != nil {
		return nil, err
	}
	for _, item := range result.Items {
		if item.Key == update.Key {
			return &item, nil
		}
	}
	return nil, ErrNotFound
}

// Commitments lists existing commitments and coverage.
func (s *Service) Commitments(ctx context.Context, projects []uuid.UUID) (*CommitmentsDTO, error) {
	db := storage.GetDb().WithContext(ctx)
	var rows []commitmentRow
	if err := db.Where("project_id IN ?", projects).
		Order("ends_at NULLS LAST, description").
		Find(&rows).
		Error; err != nil {
		return nil, err
	}
	now := s.now()
	commitments := make([]CommitmentDTO, 0, len(rows))
	for _, row := range rows {
		item := CommitmentDTO{
			ProjectID: row.ProjectID, ConnectionID: row.ConnectionID, ConnectionName: row.ConnectionName,
			Provider: row.Provider, ID: row.ID, Type: row.Type, Account: row.Account, Region: row.Region,
			Description: row.Description, Term: row.Term, Payment: row.Payment, StartsAt: row.StartsAt,
			EndsAt: row.EndsAt, Currency: row.Currency, MonthlyCost: parseAmount(row.MonthlyCost),
			Utilization: row.Utilization, UnusedMonthly: parseAmount(row.UnusedMonthly),
		}
		if row.EndsAt != nil {
			days := int(math.Ceil(row.EndsAt.Sub(now).Hours() / 24))
			item.EndsInDays = &days
			item.EndingSoon = days >= 0 && days <= endingSoonDays
		}
		item.Unused = row.Utilization != nil && *row.Utilization < unusedBelow
		commitments = append(commitments, item)
	}
	var coverage []coverageRow
	if err := db.Where("project_id IN ?", projects).Order("type").Find(&coverage).Error; err != nil {
		return nil, err
	}
	names, err := s.connectionNames(ctx, projects)
	if err != nil {
		return nil, err
	}
	covered := make([]CoverageDTO, 0, len(coverage))
	for _, row := range coverage {
		covered = append(covered, CoverageDTO{ConnectionID: row.ConnectionID, ConnectionName: names[row.ConnectionID],
			Type: row.Type, Currency: row.Currency, CoveredPercent: row.CoveredPercent,
			OnDemandMonthly: parseAmount(row.OnDemandMonthly)})
	}
	sections, err := adviceSections(ctx, projects)
	if err != nil {
		return nil, err
	}
	return &CommitmentsDTO{Commitments: commitments, Coverage: covered, Sections: sections}, nil
}

// Planner compares the providers' purchase options per connection and type for one look-back.
// With rightsizingFirst, each plan is scaled down by the open rightsizing savings on the usage
// it covers, so the planner and the rightsizing list never count the same saving twice.
func (s *Service) Planner(
	ctx context.Context, projects []uuid.UUID, lookback int, rightsizingFirst bool,
) (*PlannerDTO, error) {
	if !slices.Contains(spec.AdviceLookbacks, lookback) {
		return nil, errors.New("lookbackDays must be 7, 30 or 60")
	}
	db := storage.GetDb().WithContext(ctx)
	var rows []optionRow
	if err := db.Where("project_id IN ? AND lookback_days = ?", projects, lookback).
		Order("connection_name, type, term, payment").Find(&rows).Error; err != nil {
		return nil, err
	}
	type planKey struct {
		connection uuid.UUID
		kind       string
	}
	plans := map[planKey]*PlanDTO{}
	var order []planKey
	projectOf := map[uuid.UUID]uuid.UUID{}
	for _, row := range rows {
		key := planKey{row.ConnectionID, row.Type}
		plan := plans[key]
		if plan == nil {
			plan = &PlanDTO{ConnectionID: row.ConnectionID, ConnectionName: row.ConnectionName, Provider: row.Provider,
				Type: row.Type, LookbackDays: lookback, Factor: 1, RefreshedAt: row.RefreshedAt}
			plans[key] = plan
			order = append(order, key)
			projectOf[row.ConnectionID] = row.ProjectID
		}
		if row.Currency != "" && plan.Currency == "" {
			plan.Currency = row.Currency
		}
		plan.OnDemandMonthly = math.Max(plan.OnDemandMonthly, amountOr(parseAmount(row.OnDemandMonthly)))
		plan.Options = append(plan.Options, PurchaseOptionDTO{
			Term: row.Term, Payment: row.Payment, Offered: row.Offered, Reason: row.Reason,
			Commitment: row.Commitment, HourlyCommitment: amountOr(parseAmount(row.HourlyCommitment)),
			Upfront: amountOr(parseAmount(row.Upfront)), MonthlyRecurring: amountOr(parseAmount(row.MonthlyRecurring)),
			MonthlySavings: amountOr(parseAmount(row.MonthlySavings)), SavingsPercent: row.SavingsPercent,
			BreakEvenMonths: row.BreakEvenMonths, AverageUtilization: row.AverageUtilization,
			CoveragePercent: row.CoveragePercent, Lines: row.Lines, Labels: []string{},
		})
	}
	result := &PlannerDTO{Plans: make([]PlanDTO, 0, len(order))}
	for _, key := range order {
		plan := plans[key]
		project := projectOf[key.connection]
		rightsizing, err := openRightsizing(ctx, project, key.connection, key.kind, plan.Currency)
		if err != nil {
			return nil, err
		}
		plan.RightsizingMonthly = rightsizing
		if rightsizingFirst && rightsizing > 0 && plan.OnDemandMonthly > 0 {
			plan.Factor = math.Max(0, math.Min(1, 1-rightsizing/plan.OnDemandMonthly))
			for i := range plan.Options {
				scaleOption(&plan.Options[i], plan.Factor)
			}
		}
		labelOptions(plan.Options)
		plan.Usage, err = typeUsageSeries(ctx, project, key.connection, key.kind, lookback, s.now())
		if err != nil {
			return nil, err
		}
		result.Plans = append(result.Plans, *plan)
	}
	sections, err := adviceSections(ctx, projects)
	if err != nil {
		return nil, err
	}
	result.Sections = sections
	return result, nil
}

// ResourceRecommendations are the open recommendations for one resource (the asset drawer).
func (s *Service) ResourceRecommendations(
	ctx context.Context, projects []uuid.UUID, key string,
) ([]RecommendationDTO, error) {
	all, err := s.Recommendations(ctx, projects)
	if err != nil {
		return nil, err
	}
	keys := resourceKeys(key)
	out := []RecommendationDTO{}
	for _, item := range all.Items {
		if (item.Status == statusOpen || item.Status == statusPlanned) &&
			slices.Contains(keys, strings.TrimSpace(item.ResourceKey)) {
			out = append(out, item)
		}
	}
	return out, nil
}

// commitmentSamples are the commitments ending within endingSoonDays, valued by days left.
func commitmentSamples(ctx context.Context, projectID uuid.UUID, now time.Time) ([]alerting.SourceSample, error) {
	var rows []commitmentRow
	if err := storage.GetDb().WithContext(ctx).Where("project_id = ? AND ends_at BETWEEN ? AND ?", projectID, now,
		now.Add(endingSoonDays*24*time.Hour)).Order("ends_at").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	samples := make([]alerting.SourceSample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, alerting.SourceSample{
			Value: math.Ceil(row.EndsAt.Sub(now).Hours() / 24),
			Labels: alerting.Labels{"commitment": row.Description, "ends": row.EndsAt.Format(dayLayout),
				"connection": row.ConnectionName},
		})
	}
	return samples, nil
}

// addAgentEvidence adds the Obzly agent's CPU and memory p95 over 14 days to the connection's
// EC2 and Azure VM rightsizing recommendations, where the agent runs on the instance: the cloud's
// own metrics have no memory. A busy memory makes the recommendation risky.
func (s *Service) addAgentEvidence(ctx context.Context, projectID, connectionID uuid.UUID) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// It runs on its own after a batch is stored; a failure must not take the Site down.
	defer func() {
		if recovered := recover(); recovered != nil {
			s.log.Warn("Adding agent evidence to rightsizing failed", "error", recovered)
		}
	}()
	db := storage.GetDb().WithContext(ctx)
	var rows []recommendationRow
	if err := db.Where("project_id = ? AND connection_id = ? AND kind = ? AND resource_type IN ?", projectID,
		connectionID, spec.RecommendRightsizing, []string{"aws_ec2_instance", "azure_virtual_machine"}).
		Limit(maxAgentHosts).Find(&rows).Error; err != nil || len(rows) == 0 {
		return
	}
	hosts := map[string][]int{}
	ids := make([]string, 0, len(rows))
	for i, row := range rows {
		id := hostID(row.ResourceKey)
		if id == "" {
			continue
		}
		if _, seen := hosts[id]; !seen {
			ids = append(ids, regexQuote(id))
		}
		hosts[id] = append(hosts[id], i)
	}
	if len(ids) == 0 {
		return
	}
	matcher := `host_id=~"` + strings.Join(ids, "|") + `"`
	cpu := `1 - sum by (host_id) (rate(system_cpu_time{state="idle", ` + matcher + `}[5m])) / ` +
		`sum by (host_id) (rate(system_cpu_time{` + matcher + `}[5m]))`
	memory := `sum by (host_id) (system_memory_usage{state="used", ` + matcher + `}) / ` +
		`sum by (host_id) (system_memory_usage{` + matcher + `})`
	end := s.now()
	cpuP95, err := p95ByHost(ctx, projectID, cpu, end)
	if err != nil {
		s.log.Debug("Agent CPU for rightsizing failed", "error", err)
	}
	memoryP95, err := p95ByHost(ctx, projectID, memory, end)
	if err != nil {
		s.log.Debug("Agent memory for rightsizing failed", "error", err)
	}
	for id, indexes := range hosts {
		cpuValue, hasCPU := cpuP95[strings.ToLower(id)]
		memoryValue, hasMemory := memoryP95[strings.ToLower(id)]
		if !hasCPU && !hasMemory {
			continue
		}
		for _, i := range indexes {
			row := rows[i]
			evidence := row.Evidence
			if evidence == nil {
				evidence = jsonMap{}
			}
			risk := row.Risk
			if hasCPU {
				evidence["agent_cpu_p95"] = strconv.FormatFloat(cpuValue*100, 'f', 1, 64)
			}
			if hasMemory {
				evidence["agent_memory_p95"] = strconv.FormatFloat(memoryValue*100, 'f', 1, 64)
				if memoryValue*100 >= agentBusyMemory {
					evidence["agent_note"] = "Memory is busy on the instance: check the new size has as much memory"
					risk = "high"
				}
			}
			if err := db.Model(&recommendationRow{}).Where("project_id = ? AND connection_id = ? AND key = ?",
				projectID, connectionID, row.Key).Updates(map[string]any{"evidence": evidence, "risk": risk}).
				Error; err != nil {
				s.log.Debug("Saving agent evidence failed", "error", err)
			}
		}
	}
}

func p95ByHost(ctx context.Context, projectID uuid.UUID, expression string, end time.Time) (map[string]float64, error) {
	query, err := metrics_promql.NewRangeQuery(ctx, projectID, expression, end.Add(-agentWindow), end, agentStep)
	if err != nil {
		return nil, err
	}
	defer query.Close()
	result := query.Exec(ctx)
	if result.Err != nil {
		return nil, result.Err
	}
	matrix, ok := result.Value.(promql.Matrix)
	if !ok {
		return nil, nil
	}
	out := map[string]float64{}
	for _, series := range matrix {
		values := make([]float64, 0, len(series.Floats))
		for _, point := range series.Floats {
			if !math.IsNaN(point.F) && !math.IsInf(point.F, 0) {
				values = append(values, point.F)
			}
		}
		if len(values) < 24 {
			continue
		}
		sort.Float64s(values)
		out[strings.ToLower(series.Metric.Get("host_id"))] = values[int(math.Ceil(0.95*float64(len(values))))-1]
	}
	return out, nil
}

// openRightsizing sums open and planned rightsizing and idle savings on the resource types a
// commitment type covers, in the plan's currency.
func openRightsizing(ctx context.Context, projectID, connectionID uuid.UUID, kind, currency string) (float64, error) {
	usage, ok := typeUsage[kind]
	if !ok || currency == "" {
		return 0, nil
	}
	var total float64
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT COALESCE(SUM(r.monthly_savings), 0)::float8
		FROM cost_recommendations r
		LEFT JOIN cost_recommendation_states st ON st.project_id = r.project_id AND st.key = r.key
		WHERE r.project_id = ? AND r.connection_id = ? AND r.kind IN ('rightsizing', 'idle')
			AND r.resource_type IN ? AND r.currency = ? AND COALESCE(st.status, 'open') IN ('open', 'planned')`,
		projectID, connectionID, usage.resources, currency).Scan(&total).Error
	return total, err
}

// typeUsageSeries is the daily cost of the services a type covers over the look-back, on demand
// vs already committed.
func typeUsageSeries(
	ctx context.Context, projectID, connectionID uuid.UUID, kind string, lookback int, now time.Time,
) ([]UsagePoint, error) {
	usage, ok := typeUsage[kind]
	if !ok {
		return []UsagePoint{}, nil
	}
	today := utcDay(now)
	clauses := make([]string, 0, len(usage.services))
	args := []any{projectID, connectionID, today.AddDate(0, 0, -lookback).Format(dayLayout),
		today.AddDate(0, 0, -1).Format(dayLayout)}
	for _, pattern := range usage.services {
		clauses = append(clauses, "service ILIKE ?")
		args = append(args, pattern)
	}
	var rows []struct {
		Day       time.Time
		OnDemand  float64
		Committed float64
	}
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT day,
			COALESCE(SUM(effective) FILTER (WHERE pricing_category <> 'Committed'), 0)::float8 AS on_demand,
			COALESCE(SUM(effective) FILTER (WHERE pricing_category = 'Committed'), 0)::float8 AS committed
		FROM cloud_cost_daily WHERE project_id = ? AND connection_id = ? AND day BETWEEN ? AND ?
			AND (`+strings.Join(clauses, " OR ")+`)
		GROUP BY day ORDER BY day`, args...).Scan(&rows).Error
	points := make([]UsagePoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, UsagePoint{Day: utcDay(row.Day).Format(dayLayout), OnDemand: round2(row.OnDemand),
			Committed: round2(row.Committed)})
	}
	return points, err
}

func scaleOption(option *PurchaseOptionDTO, factor float64) {
	option.HourlyCommitment = round4(option.HourlyCommitment * factor)
	option.Upfront = round2(option.Upfront * factor)
	option.MonthlyRecurring = round2(option.MonthlyRecurring * factor)
	option.MonthlySavings = round2(option.MonthlySavings * factor)
	for i := range option.Lines {
		option.Lines[i].MonthlySavings = round2(option.Lines[i].MonthlySavings * factor)
		option.Lines[i].Upfront = round2(option.Lines[i].Upfront * factor)
	}
}

// labelOptions marks the lowest-risk option that saves anything (shortest term, least upfront)
// and the one that saves the most.
func labelOptions(options []PurchaseOptionDTO) {
	lowest, most := -1, -1
	for i, option := range options {
		if !option.Offered || option.MonthlySavings <= 0 {
			continue
		}
		if lowest < 0 || riskOrder(option) < riskOrder(options[lowest]) {
			lowest = i
		}
		if most < 0 || option.MonthlySavings > options[most].MonthlySavings {
			most = i
		}
	}
	if lowest >= 0 {
		options[lowest].Labels = append(options[lowest].Labels, "lowest_risk")
	}
	if most >= 0 {
		options[most].Labels = append(options[most].Labels, "most_savings")
	}
}

func riskOrder(option PurchaseOptionDTO) int {
	term := 0
	if option.Term == spec.Term3Years {
		term = 10
	}
	return term + paymentRisk[option.Payment]
}

func adviceSections(ctx context.Context, projects []uuid.UUID) ([]AdviceSectionDTO, error) {
	var rows []struct {
		ProjectID      uuid.UUID
		ConnectionID   uuid.UUID
		Section        string
		State          string
		Reason         string
		RefreshedAt    time.Time
		ConnectionName string
	}
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT s.project_id, s.connection_id, s.section, s.state, s.reason,
			s.refreshed_at, COALESCE(
			(SELECT connection_name FROM cost_recommendations r WHERE r.connection_id = s.connection_id LIMIT 1),
			(SELECT connection_name FROM cost_purchase_options o WHERE o.connection_id = s.connection_id LIMIT 1),
			(SELECT connection_name FROM cost_commitments c WHERE c.connection_id = s.connection_id LIMIT 1),
			(SELECT connection_name FROM cloud_cost_sources x WHERE x.connection_id = s.connection_id LIMIT 1),
			'') AS connection_name
		FROM cost_advice_sections s WHERE s.project_id IN ? ORDER BY connection_name, section`, projects).
		Scan(&rows).Error
	sections := make([]AdviceSectionDTO, 0, len(rows))
	for _, row := range rows {
		sections = append(sections, AdviceSectionDTO{ProjectID: row.ProjectID, ConnectionID: row.ConnectionID,
			ConnectionName: row.ConnectionName, Section: row.Section, State: row.State, Reason: row.Reason,
			RefreshedAt: row.RefreshedAt})
	}
	return sections, err
}

func applyState(item *RecommendationDTO, state recommendationState) {
	item.Status, item.Owner, item.Reason = state.Status, state.Owner, state.Reason
	if state.DueDate != nil {
		item.DueDate = state.DueDate.Format(dayLayout)
	}
	item.DoneAt, item.DoneBy, item.MeasuredAt = state.DoneAt, state.DoneBy, state.MeasuredAt
	item.ExpectedMonthly, item.RealizedMonthly = state.ExpectedMonthly, state.RealizedMonthly
}

// savingsBasis says where a recommendation's savings come from: the provider's estimate, the
// resource's own cost (Obzly checks), or the list price (Elastic IPs).
func savingsBasis(row recommendationRow) string {
	switch {
	case row.Source != spec.RecommendFromObzly:
		return "provider"
	case row.SavingsShare > 0:
		return "resource_cost"
	}
	return "list_price"
}

// hostID is the instance ID the agent reports as host_id: an EC2 instance's ID, an Azure VM's
// name is not reliable, so only EC2 joins.
func hostID(resourceKey string) string {
	if match := awsInstanceARN.FindStringSubmatch(resourceKey); match != nil {
		return match[1]
	}
	if strings.HasPrefix(resourceKey, "i-") {
		return resourceKey
	}
	return ""
}

func regexQuote(value string) string {
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func parseAmount(text *string) *float64 {
	if text == nil {
		return nil
	}
	value, err := strconv.ParseFloat(*text, 64)
	if err != nil {
		return nil
	}
	value = round2(value)
	return &value
}

func amountOr(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }
func round4(value float64) float64 { return math.Round(value*10000) / 10000 }
