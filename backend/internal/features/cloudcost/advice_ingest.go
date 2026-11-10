package cloudcost

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

// Cost advice on the Site (docs/CLOUD_COST_POSTURE.md "Recommendations"): the probe's weekly
// recommendations, commitments and purchase options per connection; people's decisions about
// recommendations; realized savings measured from cost data.

const (
	statusOpen      = "open"
	statusPlanned   = "planned"
	statusDone      = "done"
	statusDismissed = "dismissed"
	doneByDetected  = "detected"

	// realizeAfter: realized savings compare the 14 days before a change with the 14 after, once
	// the data for those days has arrived.
	realizeWindowDays = 14
	realizeAfter      = 16 * 24 * time.Hour
	// minRealizeDays: a window with fewer days of cost can't say what changed.
	minRealizeDays = 7
	// resourceCostDays: share-based savings come from the resource's cost over these days.
	resourceCostDays = 30
	maxAdviceText    = 1000
)

var (
	adviceKinds = []string{spec.RecommendRightsizing, spec.RecommendIdle, spec.RecommendModernize,
		spec.RecommendStorage}
	adviceSources = []string{spec.RecommendFromComputeOptimizer, spec.RecommendFromAdvisor, spec.RecommendFromObzly}
	adviceLevels  = []string{"low", "medium", "high"}
	adviceTypes   = []string{spec.CommitComputeSP, spec.CommitEC2SP, spec.CommitEC2RI, spec.CommitRDSRI,
		spec.CommitAzureSP, spec.CommitAzureReserve}
	// sectionSources: the recommendation sources each section replaces.
	sectionSources = map[string][]string{
		spec.AdviceRightsizing: {spec.RecommendFromComputeOptimizer, spec.RecommendFromAdvisor},
		spec.AdviceChecks:      {spec.RecommendFromObzly},
	}
)

type recommendationRow struct {
	ProjectID      uuid.UUID `gorm:"column:project_id"`
	ConnectionID   uuid.UUID `gorm:"column:connection_id"`
	Key            string    `gorm:"column:key"`
	ConnectionName string    `gorm:"column:connection_name"`
	Provider       string    `gorm:"column:provider"`
	Source         string    `gorm:"column:source"`
	Kind           string    `gorm:"column:kind"`
	Account        string    `gorm:"column:account"`
	Region         string    `gorm:"column:region"`
	ResourceKey    string    `gorm:"column:resource_key"`
	ResourceName   string    `gorm:"column:resource_name"`
	ResourceType   string    `gorm:"column:resource_type"`
	Action         string    `gorm:"column:action"`
	Current        string    `gorm:"column:current_config"`
	Target         string    `gorm:"column:target_config"`
	Currency       string    `gorm:"column:currency"`
	MonthlySavings *string   `gorm:"column:monthly_savings"`
	SavingsShare   float64   `gorm:"column:savings_share"`
	Effort         string    `gorm:"column:effort"`
	Risk           string    `gorm:"column:risk"`
	Evidence       jsonMap   `gorm:"column:evidence"`
	RefreshedAt    time.Time `gorm:"column:refreshed_at"`
}

func (recommendationRow) TableName() string { return "cost_recommendations" }

type recommendationState struct {
	ProjectID       uuid.UUID  `gorm:"column:project_id;primaryKey"`
	Key             string     `gorm:"column:key;primaryKey"`
	Status          string     `gorm:"column:status"`
	Owner           string     `gorm:"column:owner"`
	DueDate         *time.Time `gorm:"column:due_date"`
	Reason          string     `gorm:"column:reason"`
	Action          string     `gorm:"column:action"`
	ResourceKey     string     `gorm:"column:resource_key"`
	ResourceName    string     `gorm:"column:resource_name"`
	Currency        string     `gorm:"column:currency"`
	ExpectedMonthly *float64   `gorm:"column:expected_monthly"`
	DoneAt          *time.Time `gorm:"column:done_at"`
	DoneBy          string     `gorm:"column:done_by"`
	RealizedMonthly *float64   `gorm:"column:realized_monthly"`
	MeasuredAt      *time.Time `gorm:"column:measured_at"`
	UpdatedBy       string     `gorm:"column:updated_by"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (recommendationState) TableName() string { return "cost_recommendation_states" }

type commitmentRow struct {
	ProjectID      uuid.UUID  `gorm:"column:project_id"`
	ConnectionID   uuid.UUID  `gorm:"column:connection_id"`
	ID             string     `gorm:"column:id"`
	ConnectionName string     `gorm:"column:connection_name"`
	Provider       string     `gorm:"column:provider"`
	Type           string     `gorm:"column:type"`
	Account        string     `gorm:"column:account"`
	Region         string     `gorm:"column:region"`
	Description    string     `gorm:"column:description"`
	Term           string     `gorm:"column:term"`
	Payment        string     `gorm:"column:payment"`
	StartsAt       *time.Time `gorm:"column:starts_at"`
	EndsAt         *time.Time `gorm:"column:ends_at"`
	Currency       string     `gorm:"column:currency"`
	MonthlyCost    *string    `gorm:"column:monthly_cost"`
	Utilization    *float64   `gorm:"column:utilization"`
	UnusedMonthly  *string    `gorm:"column:unused_monthly"`
	RefreshedAt    time.Time  `gorm:"column:refreshed_at"`
}

func (commitmentRow) TableName() string { return "cost_commitments" }

type coverageRow struct {
	ProjectID       uuid.UUID `gorm:"column:project_id"`
	ConnectionID    uuid.UUID `gorm:"column:connection_id"`
	Type            string    `gorm:"column:type"`
	Currency        string    `gorm:"column:currency"`
	CoveredPercent  float64   `gorm:"column:covered_percent"`
	OnDemandMonthly *string   `gorm:"column:on_demand_monthly"`
	RefreshedAt     time.Time `gorm:"column:refreshed_at"`
}

func (coverageRow) TableName() string { return "cost_commitment_coverage" }

type optionRow struct {
	ProjectID          uuid.UUID     `gorm:"column:project_id"`
	ConnectionID       uuid.UUID     `gorm:"column:connection_id"`
	Type               string        `gorm:"column:type"`
	Term               string        `gorm:"column:term"`
	Payment            string        `gorm:"column:payment"`
	LookbackDays       int           `gorm:"column:lookback_days"`
	ConnectionName     string        `gorm:"column:connection_name"`
	Provider           string        `gorm:"column:provider"`
	Offered            bool          `gorm:"column:offered"`
	Reason             string        `gorm:"column:reason"`
	Currency           string        `gorm:"column:currency"`
	Commitment         string        `gorm:"column:commitment"`
	HourlyCommitment   *string       `gorm:"column:hourly_commitment"`
	Upfront            *string       `gorm:"column:upfront"`
	MonthlyRecurring   *string       `gorm:"column:monthly_recurring"`
	MonthlySavings     *string       `gorm:"column:monthly_savings"`
	OnDemandMonthly    *string       `gorm:"column:on_demand_monthly"`
	SavingsPercent     float64       `gorm:"column:savings_percent"`
	BreakEvenMonths    float64       `gorm:"column:break_even_months"`
	AverageUtilization float64       `gorm:"column:average_utilization"`
	CoveragePercent    float64       `gorm:"column:coverage_percent"`
	Lines              purchaseLines `gorm:"column:lines"`
	RefreshedAt        time.Time     `gorm:"column:refreshed_at"`
}

func (optionRow) TableName() string { return "cost_purchase_options" }

type adviceSectionRow struct {
	ProjectID    uuid.UUID `gorm:"column:project_id;primaryKey"`
	ConnectionID uuid.UUID `gorm:"column:connection_id;primaryKey"`
	Section      string    `gorm:"column:section;primaryKey"`
	State        string    `gorm:"column:state"`
	Reason       string    `gorm:"column:reason"`
	RefreshedAt  time.Time `gorm:"column:refreshed_at"`
}

func (adviceSectionRow) TableName() string { return "cost_advice_sections" }

// PurchaseLineDTO is one item of a purchase option, amounts in its currency.
type PurchaseLineDTO struct {
	Description    string  `json:"description"`
	Account        string  `json:"account,omitempty"`
	Region         string  `json:"region,omitempty"`
	Quantity       string  `json:"quantity,omitempty"`
	MonthlySavings float64 `json:"monthlySavings"`
	Upfront        float64 `json:"upfront"`
}

type purchaseLines []PurchaseLineDTO

func (l purchaseLines) Value() (driver.Value, error) {
	if l == nil {
		return "[]", nil
	}
	return jsonValue(l)
}
func (l *purchaseLines) Scan(value any) error { return scanJSON(value, l) }

// ErrInvalidAdvice: the probe sent advice this Site won't store.
var ErrInvalidAdvice = errors.New("invalid advice batch")

// ReceiveAdvice stores one chunk of an advice batch; once all chunks are here it applies it.
func (s *Service) ReceiveAdvice(ctx context.Context, projectID uuid.UUID, batch spec.AdviceBatch) (bool, error) {
	if err := validateAdvice(batch); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidAdvice, err)
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return false, err
	}
	db := storage.GetDb().WithContext(ctx)
	err = db.Exec(`INSERT INTO cost_advice_batches (batch_id, seq, project_id, total, payload) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (batch_id, seq) DO NOTHING`, batch.BatchID, batch.Seq, projectID, batch.Total, string(payload)).Error
	if err != nil {
		return false, err
	}
	var received int64
	if err := db.Raw(`SELECT count(*) FROM cost_advice_batches WHERE batch_id = ? AND project_id = ?`,
		batch.BatchID, projectID).Scan(&received).Error; err != nil {
		return false, err
	}
	if received < int64(batch.Total) {
		return false, nil
	}
	applied, connectionID, err := s.applyAdvice(ctx, projectID, batch.BatchID)
	if applied && err == nil {
		go s.addAgentEvidence(context.WithoutCancel(ctx), projectID, connectionID)
	}
	return applied, err
}

func (s *Service) applyAdvice(ctx context.Context, projectID uuid.UUID, batchID string) (bool, uuid.UUID, error) {
	applied, connectionID := false, uuid.Nil
	err := storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "cost-advice:"+batchID).Error; err != nil {
			return err
		}
		var payloads []string
		if err := tx.Raw(`SELECT payload FROM cost_advice_batches WHERE batch_id = ? AND project_id = ? ORDER BY seq`,
			batchID, projectID).Scan(&payloads).Error; err != nil {
			return err
		}
		if len(payloads) == 0 {
			return nil
		}
		var merged spec.AdviceBatch
		for _, payload := range payloads {
			var chunk spec.AdviceBatch
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return err
			}
			if merged.BatchID == "" {
				merged = chunk
				continue
			}
			merged.Recommendations = append(merged.Recommendations, chunk.Recommendations...)
		}
		if len(payloads) != merged.Total {
			return nil
		}
		connectionID = uuid.MustParse(merged.ConnectionID)
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`,
			"cost-advice:"+projectID.String()+":"+connectionID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM cost_advice_batches WHERE batch_id = ? AND project_id = ?`, batchID,
			projectID).Error; err != nil {
			return err
		}
		applied = true
		return s.mergeAdvice(tx, projectID, connectionID, merged)
	})
	return applied, connectionID, err
}

// mergeAdvice replaces the connection's data for each section that came back ok; a section
// that didn't keeps its last good data, and nothing it had counts as done.
func (s *Service) mergeAdvice(tx *gorm.DB, projectID, connectionID uuid.UUID, batch spec.AdviceBatch) error {
	refreshed := batch.GeneratedAt.UTC()
	if refreshed.IsZero() || refreshed.After(s.now()) {
		refreshed = s.now()
	}
	ok := map[string]bool{}
	for _, section := range batch.Sections {
		ok[section.Section] = section.State == spec.AdviceOK
		row := adviceSectionRow{ProjectID: projectID, ConnectionID: connectionID, Section: section.Section,
			State: section.State, Reason: clipAdvice(section.Reason, 500), RefreshedAt: refreshed}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}

	var sources []string
	for section, list := range sectionSources {
		if ok[section] {
			sources = append(sources, list...)
		}
	}
	if len(sources) > 0 {
		if err := s.replaceRecommendations(tx, projectID, connectionID, batch, sources, refreshed); err != nil {
			return err
		}
	}
	if ok[spec.AdviceCommitments] {
		if err := replaceCommitments(tx, projectID, connectionID, batch, refreshed); err != nil {
			return err
		}
	}
	if ok[spec.AdvicePlanner] {
		if err := replaceOptions(tx, projectID, connectionID, batch, refreshed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) replaceRecommendations(
	tx *gorm.DB, projectID, connectionID uuid.UUID, batch spec.AdviceBatch, sources []string, refreshed time.Time,
) error {
	var previous []recommendationRow
	if err := tx.Where("project_id = ? AND connection_id = ? AND source IN ?", projectID, connectionID, sources).
		Find(&previous).Error; err != nil {
		return err
	}
	rows := make([]recommendationRow, 0, len(batch.Recommendations))
	fresh := map[string]bool{}
	for _, rec := range batch.Recommendations {
		if !slices.Contains(sources, rec.Source) || fresh[rec.Key] {
			continue
		}
		fresh[rec.Key] = true
		row := recommendationRow{
			ProjectID: projectID, ConnectionID: connectionID, Key: rec.Key, ConnectionName: batch.ConnectionName,
			Provider: batch.Provider, Source: rec.Source, Kind: rec.Kind, Account: rec.Account, Region: rec.Region,
			ResourceKey: clipAdvice(rec.ResourceKey, 2048), ResourceName: clipAdvice(rec.ResourceName, 300),
			ResourceType: clipAdvice(rec.ResourceType, 100), Action: clipAdvice(rec.Action, maxAdviceText),
			Current: clipAdvice(rec.Current, 200), Target: clipAdvice(rec.Target, 200),
			Currency: strings.ToUpper(rec.Currency), SavingsShare: rec.SavingsShare,
			Effort: oneOf(rec.Effort, adviceLevels, "medium"), Risk: oneOf(rec.Risk, adviceLevels, "low"),
			Evidence: boundedMap(rec.Evidence), RefreshedAt: refreshed,
		}
		if rec.MonthlySavingsMicros > 0 && currencyPattern.MatchString(row.Currency) {
			text := microsText(rec.MonthlySavingsMicros)
			row.MonthlySavings = &text
		} else if rec.SavingsShare > 0 {
			currency, monthly, found, err := resourceMonthlyCost(tx, projectID, rec.ResourceKey, refreshed)
			if err != nil {
				return err
			}
			if found {
				text := fmt.Sprintf("%.6f", monthly*min(rec.SavingsShare, 1))
				row.MonthlySavings, row.Currency = &text, currency
			}
		}
		rows = append(rows, row)
	}
	if err := tx.Where("project_id = ? AND connection_id = ? AND source IN ?", projectID, connectionID, sources).
		Delete(&recommendationRow{}).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		if err := tx.CreateInBatches(rows, 500).Error; err != nil {
			return err
		}
	}

	// A recommendation the provider no longer makes was acted on; one that comes back after being
	// detected as done wasn't (a done one someone marked stays done).
	for _, row := range previous {
		if fresh[row.Key] {
			continue
		}
		var expected *float64
		if row.MonthlySavings != nil {
			if value, err := strconv.ParseFloat(*row.MonthlySavings, 64); err == nil {
				expected = &value
			}
		}
		if err := tx.Exec(`INSERT INTO cost_recommendation_states (project_id, key, status, action, resource_key,
				resource_name, currency, expected_monthly, done_at, done_by, updated_at)
			VALUES (?, ?, 'done', ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, key) DO UPDATE SET status = 'done', done_at = EXCLUDED.done_at,
				done_by = EXCLUDED.done_by, currency = EXCLUDED.currency,
				expected_monthly = COALESCE(cost_recommendation_states.expected_monthly, EXCLUDED.expected_monthly),
				updated_at = EXCLUDED.updated_at
			WHERE cost_recommendation_states.status IN ('open', 'planned')`,
			projectID, row.Key, row.Action, row.ResourceKey, row.ResourceName, row.Currency, expected, refreshed,
			doneByDetected, refreshed).Error; err != nil {
			return err
		}
	}
	back := make([]string, 0, len(fresh))
	for key := range fresh {
		back = append(back, key)
	}
	if len(back) > 0 {
		return tx.Exec(`UPDATE cost_recommendation_states SET status = 'open', done_at = NULL, done_by = '',
				realized_monthly = NULL, measured_at = NULL, updated_at = ?
			WHERE project_id = ? AND key IN ? AND status = 'done' AND done_by = ?`,
			refreshed, projectID, back, doneByDetected).Error
	}
	return nil
}

func replaceCommitments(
	tx *gorm.DB, projectID, connectionID uuid.UUID, batch spec.AdviceBatch, refreshed time.Time,
) error {
	if err := tx.Where("project_id = ? AND connection_id = ?", projectID, connectionID).
		Delete(&commitmentRow{}).Error; err != nil {
		return err
	}
	if err := tx.Where("project_id = ? AND connection_id = ?", projectID, connectionID).
		Delete(&coverageRow{}).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	rows := make([]commitmentRow, 0, len(batch.Commitments))
	for _, commitment := range batch.Commitments {
		if commitment.ID == "" || seen[commitment.ID] || !slices.Contains(adviceTypes, commitment.Type) {
			continue
		}
		seen[commitment.ID] = true
		rows = append(rows, commitmentRow{
			ProjectID: projectID, ConnectionID: connectionID, ID: clipAdvice(commitment.ID, 500),
			ConnectionName: batch.ConnectionName, Provider: batch.Provider, Type: commitment.Type,
			Account: commitment.Account, Region: commitment.Region,
			Description: clipAdvice(commitment.Description, 500), Term: commitment.Term, Payment: commitment.Payment,
			StartsAt: commitment.Start, EndsAt: commitment.End, Currency: strings.ToUpper(commitment.Currency),
			MonthlyCost: optionalMicros(commitment.MonthlyCostMicros), Utilization: commitment.UtilizationPercent,
			UnusedMonthly: optionalMicros(commitment.UnusedMonthlyMicros), RefreshedAt: refreshed,
		})
	}
	if len(rows) > 0 {
		if err := tx.CreateInBatches(rows, 500).Error; err != nil {
			return err
		}
	}
	coverage := make([]coverageRow, 0, len(batch.Coverage))
	for _, row := range batch.Coverage {
		if !slices.Contains(adviceTypes, row.Type) || slices.ContainsFunc(coverage,
			func(c coverageRow) bool { return c.Type == row.Type }) {
			continue
		}
		coverage = append(coverage, coverageRow{ProjectID: projectID, ConnectionID: connectionID, Type: row.Type,
			Currency: strings.ToUpper(row.Currency), CoveredPercent: math.Max(0, math.Min(row.CoveredPercent, 100)),
			OnDemandMonthly: optionalMicros(row.OnDemandMonthlyMicros), RefreshedAt: refreshed})
	}
	if len(coverage) > 0 {
		return tx.Create(&coverage).Error
	}
	return nil
}

func replaceOptions(tx *gorm.DB, projectID, connectionID uuid.UUID, batch spec.AdviceBatch, refreshed time.Time) error {
	if err := tx.Where("project_id = ? AND connection_id = ?", projectID, connectionID).
		Delete(&optionRow{}).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	rows := make([]optionRow, 0, len(batch.Options))
	for _, option := range batch.Options {
		key := fmt.Sprintf("%s|%s|%s|%d", option.Type, option.Term, option.Payment, option.LookbackDays)
		if seen[key] || !slices.Contains(adviceTypes, option.Type) || !slices.Contains(spec.AdviceLookbacks,
			option.LookbackDays) {
			continue
		}
		seen[key] = true
		lines := make(purchaseLines, 0, len(option.Lines))
		for _, line := range option.Lines {
			lines = append(lines, PurchaseLineDTO{Description: clipAdvice(line.Description, 300),
				Account: line.Account, Region: line.Region, Quantity: line.Quantity,
				MonthlySavings: float64(line.MonthlySavingsMicros) / 1e6, Upfront: float64(line.UpfrontMicros) / 1e6})
		}
		rows = append(rows, optionRow{
			ProjectID:        projectID,
			ConnectionID:     connectionID,
			Type:             option.Type,
			Term:             option.Term,
			Payment:          option.Payment,
			LookbackDays:     option.LookbackDays,
			ConnectionName:   batch.ConnectionName,
			Provider:         batch.Provider,
			Offered:          option.Offered,
			Reason:           clipAdvice(option.Reason, 300),
			Currency:         strings.ToUpper(option.Currency),
			Commitment:       clipAdvice(option.Commitment, 300),
			HourlyCommitment: optionalMicros(option.HourlyCommitmentMicros),
			Upfront: optionalMicros(
				option.UpfrontMicros,
			),
			MonthlyRecurring:   optionalMicros(option.MonthlyRecurringMicros),
			MonthlySavings:     optionalMicros(option.MonthlySavingsMicros),
			OnDemandMonthly:    optionalMicros(option.OnDemandMonthlyMicros),
			SavingsPercent:     option.SavingsPercent,
			BreakEvenMonths:    option.BreakEvenMonths,
			AverageUtilization: option.AverageUtilization,
			CoveragePercent:    option.CoveragePercent,
			Lines:              lines,
			RefreshedAt:        refreshed,
		})
	}
	if len(rows) > 0 {
		return tx.CreateInBatches(rows, 500).Error
	}
	return nil
}

// resourceMonthlyCost is a resource's effective cost over the last resourceCostDays, scaled to a
// month, in its main currency; false when the exports have no cost for it.
func resourceMonthlyCost(tx *gorm.DB, projectID uuid.UUID, key string, now time.Time) (string, float64, bool, error) {
	today := utcDay(now)
	var rows []struct {
		Currency string
		Amount   float64
		Days     int
	}
	err := tx.Raw(`SELECT currency, SUM(effective)::float8 AS amount, count(DISTINCT day) AS days
		FROM cloud_cost_daily WHERE project_id = ? AND source = 'export' AND resource IN ? AND day BETWEEN ? AND ?
		GROUP BY currency ORDER BY amount DESC LIMIT 1`, projectID, resourceKeys(key),
		today.AddDate(0, 0, -resourceCostDays).Format(dayLayout), today.Format(dayLayout)).Scan(&rows).Error
	if err != nil || len(rows) == 0 || rows[0].Days == 0 {
		return "", 0, false, err
	}
	return rows[0].Currency, rows[0].Amount * daysPerMonth / float64(rows[0].Days), true, nil
}

// realizeSavings measures done recommendations once the 14 days after the change have cost:
// the median daily cost before minus after, per month. Without enough cost data it stays n/a.
func (s *Service) realizeSavings(ctx context.Context, now time.Time) error {
	db := storage.GetDb().WithContext(ctx)
	var states []recommendationState
	if err := db.Where("status = ? AND measured_at IS NULL AND done_at <= ?", statusDone, now.Add(-realizeAfter)).
		Limit(500).Find(&states).Error; err != nil {
		return err
	}
	for _, state := range states {
		done := utcDay(*state.DoneAt)
		median := func(from, to time.Time) (float64, int, error) {
			var days []float64
			err := db.Raw(`SELECT SUM(effective)::float8 FROM cloud_cost_daily
				WHERE project_id = ? AND source = 'export' AND resource IN ? AND currency = ? AND day BETWEEN ? AND ?
				GROUP BY day`, state.ProjectID, resourceKeys(state.ResourceKey), state.Currency,
				from.Format(dayLayout), to.Format(dayLayout)).Scan(&days).Error
			return medianOf(days), len(days), err
		}
		before, beforeDays, err := median(done.AddDate(0, 0, -realizeWindowDays), done.AddDate(0, 0, -1))
		if err != nil {
			return err
		}
		after, afterDays, err := median(done, done.AddDate(0, 0, realizeWindowDays-1))
		if err != nil {
			return err
		}
		// Gone resources have no rows after the change: they cost nothing.
		if afterDays == 0 && beforeDays >= minRealizeDays {
			afterDays = minRealizeDays
		}
		var realized *float64
		if beforeDays >= minRealizeDays && afterDays >= minRealizeDays {
			value := math.Round((before-after)*daysPerMonth*100) / 100
			realized = &value
		}
		if err := db.Model(&recommendationState{}).Where("project_id = ? AND key = ?", state.ProjectID, state.Key).
			Updates(map[string]any{"realized_monthly": realized, "measured_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateAdvice(batch spec.AdviceBatch) error {
	if batch.BatchID == "" || len(batch.BatchID) > 64 {
		return errors.New("no batch ID")
	}
	if batch.Total < 1 || batch.Total > spec.MaxAdviceChunks || batch.Seq < 1 || batch.Seq > batch.Total {
		return errors.New("bad chunk numbering")
	}
	if _, err := uuid.Parse(batch.ConnectionID); err != nil {
		return errors.New("the connection ID isn't a UUID")
	}
	if batch.Provider != spec.ProviderAWS && batch.Provider != spec.ProviderAzure {
		return errors.New("provider must be aws or azure")
	}
	for _, section := range batch.Sections {
		if !slices.Contains([]string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments,
			spec.AdvicePlanner}, section.Section) || sectionStates[section.State] == "" {
			return fmt.Errorf("unknown section %q or state %q", section.Section, section.State)
		}
	}
	for _, rec := range batch.Recommendations {
		if rec.Key == "" || len(rec.Key) > 2100 || rec.ResourceKey == "" || rec.Action == "" ||
			!slices.Contains(adviceKinds, rec.Kind) || !slices.Contains(adviceSources, rec.Source) {
			return errors.New("a recommendation needs a key, resource, action, known kind and source")
		}
		if rec.MonthlySavingsMicros < 0 || rec.SavingsShare < 0 || rec.SavingsShare > 1 {
			return errors.New("a recommendation's savings can't be negative")
		}
	}
	return nil
}

var sectionStates = map[string]string{spec.AdviceOK: "ok", spec.AdviceNotEnabled: "not enabled",
	spec.AdviceNeedsPermission: "needs permission", spec.AdviceError: "error"}

func dropStaleAdvice(ctx context.Context, now time.Time) error {
	return storage.GetDb().WithContext(ctx).
		Exec(`DELETE FROM cost_advice_batches WHERE received_at < ?`, now.Add(-stagedFor)).Error
}

func optionalMicros(micros int64) *string {
	if micros == 0 {
		return nil
	}
	text := microsText(micros)
	return &text
}

func boundedMap(values map[string]string) jsonMap {
	out := jsonMap{}
	for key, value := range values {
		if len(out) >= 20 {
			break
		}
		out[clipAdvice(key, 100)] = clipAdvice(value, 500)
	}
	return out
}

func oneOf(value string, allowed []string, fallback string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return fallback
}

func clipAdvice(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		return text[:limit]
	}
	return text
}
