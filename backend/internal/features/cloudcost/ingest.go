package cloudcost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

const (
	maxBatchChunks  = 1000
	maxBatchDays    = 400
	insertBatchSize = 2000
	// stagedFor: chunks of a batch that never completes are dropped after this.
	stagedFor = 24 * time.Hour
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// ErrInvalidBatch: the probe sent something this Site won't store.
var ErrInvalidBatch = errors.New("invalid cost batch")

// Receive stores one chunk of a batch for project; once all chunks are here it applies the batch.
// It reports whether the batch was applied.
func (s *Service) Receive(ctx context.Context, projectID uuid.UUID, batch spec.CostBatch) (bool, error) {
	if err := validateBatch(batch); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidBatch, err)
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return false, err
	}
	db := storage.GetDb().WithContext(ctx)
	err = db.Exec(`INSERT INTO cloud_cost_batches (batch_id, seq, project_id, total, payload) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (batch_id, seq) DO NOTHING`, batch.BatchID, batch.Seq, projectID, batch.Total, string(payload)).Error
	if err != nil {
		return false, err
	}
	var received int64
	if err := db.Raw(`SELECT count(*) FROM cloud_cost_batches WHERE batch_id = ? AND project_id = ?`,
		batch.BatchID, projectID).Scan(&received).Error; err != nil {
		return false, err
	}
	if received < int64(batch.Total) {
		return false, nil
	}
	return s.apply(ctx, projectID, batch.BatchID)
}

// apply replaces the batch's days with its rows in one transaction; a concurrent apply of the
// same batch finds its chunks gone and does nothing.
func (s *Service) apply(ctx context.Context, projectID uuid.UUID, batchID string) (bool, error) {
	var applied *spec.CostBatch
	err := storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "cloud-cost:"+batchID).Error; err != nil {
			return err
		}
		var payloads []string
		if err := tx.Raw(`SELECT payload FROM cloud_cost_batches WHERE batch_id = ? AND project_id = ? ORDER BY seq`,
			batchID, projectID).Scan(&payloads).Error; err != nil {
			return err
		}
		if len(payloads) == 0 {
			return nil
		}
		batch, err := joinChunks(payloads)
		if err != nil {
			return err
		}
		if len(payloads) != batch.Total {
			return nil
		}
		// One connection's batches apply one at a time, so a slow one can't land after a newer one.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`,
			"cloud-cost-connection:"+batch.ConnectionID+":"+batch.Source).Error; err != nil {
			return err
		}
		if err := replaceDays(tx, projectID, batch); err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM cloud_cost_batches WHERE batch_id = ?`, batchID).Error; err != nil {
			return err
		}
		applied = batch
		return nil
	})
	if err != nil || applied == nil {
		return false, err
	}
	if applied.From != "" {
		connectionID, _ := uuid.Parse(applied.ConnectionID)
		if err := s.detectAnomalies(ctx, projectID, connectionID, s.now()); err != nil {
			s.log.Warn("Cost anomaly detection failed", "connection", applied.ConnectionName, "error", err)
		}
	}
	return true, nil
}

func joinChunks(payloads []string) (*spec.CostBatch, error) {
	var joined *spec.CostBatch
	for _, payload := range payloads {
		var chunk spec.CostBatch
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, err
		}
		if joined == nil {
			joined = &chunk
			continue
		}
		joined.Rows = append(joined.Rows, chunk.Rows...)
		if len(chunk.Forecast) > 0 {
			joined.Forecast = chunk.Forecast
		}
	}
	return joined, nil
}

func replaceDays(tx *gorm.DB, projectID uuid.UUID, batch *spec.CostBatch) error {
	connectionID := uuid.MustParse(batch.ConnectionID)
	if batch.From != "" {
		// Export rows replace both sources for their days: a day holds one source.
		sources := []string{batch.Source}
		if batch.Source == spec.CostSourceExport {
			sources = append(sources, spec.CostSourceAPI)
		}
		if err := tx.Exec(`DELETE FROM cloud_cost_daily WHERE project_id = ? AND connection_id = ? AND source IN ?
			AND day BETWEEN ? AND ?`, projectID, connectionID, sources, batch.From, batch.To).Error; err != nil {
			return err
		}
		rows := make([]costDaily, 0, len(batch.Rows))
		for _, row := range batch.Rows {
			if row.Day < batch.From || row.Day > batch.To {
				continue
			}
			rows = append(rows, costDaily{
				ProjectID: projectID, ConnectionID: connectionID, Source: batch.Source, Day: row.Day,
				Provider: batch.Provider, BillingAccount: clip(row.BillingAccount), SubAccount: clip(row.SubAccount),
				SubAccountName: clip(row.SubAccountName), Service: clip(row.Service), Region: clip(row.Region),
				Resource: clipLong(row.Resource), ChargeCategory: clip(row.ChargeCategory),
				PricingCategory: clip(row.PricingCategory), Currency: row.Currency,
				Billed: microsText(row.BilledMicros), Effective: microsText(row.EffectiveMicros),
				List: microsText(row.ListMicros), Tags: jsonMap(row.Tags), Estimated: row.Estimated,
			})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, insertBatchSize).Error; err != nil {
				return err
			}
		}
		if batch.Source == spec.CostSourceAPI {
			// Days the export already covers stay with the export.
			if err := tx.Exec(`DELETE FROM cloud_cost_daily d WHERE d.project_id = ? AND d.connection_id = ?
				AND d.source = 'api' AND d.day BETWEEN ? AND ? AND EXISTS (SELECT 1 FROM cloud_cost_daily e
				WHERE e.project_id = d.project_id AND e.connection_id = d.connection_id AND e.source = 'export'
				AND e.day = d.day)`, projectID, connectionID, batch.From, batch.To).Error; err != nil {
				return err
			}
		}
	}
	var from, to, finalThrough any
	if batch.From != "" {
		from, to = batch.From, batch.To
	}
	if batch.FinalThrough != "" {
		finalThrough = batch.FinalThrough
	}
	err := tx.Exec(`INSERT INTO cloud_cost_sources (project_id, connection_id, source, connection_name, provider,
			from_day, to_day, final_through, rows, covered_from, covered_to, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
		ON CONFLICT (project_id, connection_id, source) DO UPDATE SET
			connection_name = EXCLUDED.connection_name, provider = EXCLUDED.provider,
			from_day = COALESCE(EXCLUDED.from_day, cloud_cost_sources.from_day),
			to_day = COALESCE(EXCLUDED.to_day, cloud_cost_sources.to_day),
			final_through = COALESCE(EXCLUDED.final_through, cloud_cost_sources.final_through),
			rows = CASE WHEN EXCLUDED.from_day IS NULL THEN cloud_cost_sources.rows ELSE EXCLUDED.rows END,
			covered_from = LEAST(cloud_cost_sources.covered_from, EXCLUDED.covered_from),
			covered_to = GREATEST(cloud_cost_sources.covered_to, EXCLUDED.covered_to),
			delivered_at = NOW()`,
		projectID, connectionID, batch.Source, clip(batch.ConnectionName), batch.Provider, from, to, finalThrough,
		len(batch.Rows), from, to).Error
	if err != nil {
		return err
	}
	for _, forecast := range batch.Forecast {
		month, err := time.Parse("2006-01", forecast.Month)
		if err != nil || !currencyPattern.MatchString(forecast.Currency) {
			continue
		}
		err = tx.Exec(`INSERT INTO cloud_cost_forecasts (project_id, connection_id, month, currency, actual, remaining,
				updated_at) VALUES (?, ?, ?, ?, 0, ?, NOW())
			ON CONFLICT (project_id, connection_id, month, currency) DO UPDATE SET
				remaining = EXCLUDED.remaining, updated_at = NOW()`,
			projectID, connectionID, month.Format(dayLayout), forecast.Currency,
			microsText(forecast.RemainingMicros)).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func validateBatch(batch spec.CostBatch) error {
	if batch.BatchID == "" || len(batch.BatchID) > 64 {
		return errors.New("no batch ID")
	}
	if batch.Total < 1 || batch.Total > maxBatchChunks || batch.Seq < 1 || batch.Seq > batch.Total {
		return errors.New("bad chunk numbering")
	}
	if _, err := uuid.Parse(batch.ConnectionID); err != nil {
		return errors.New("the connection ID isn't a UUID")
	}
	if batch.Source != spec.CostSourceExport && batch.Source != spec.CostSourceAPI {
		return errors.New("source must be export or api")
	}
	if batch.Provider != spec.ProviderAWS && batch.Provider != spec.ProviderAzure {
		return errors.New("provider must be aws or azure")
	}
	if batch.From != "" {
		from, err := parseDay(batch.From)
		if err != nil {
			return err
		}
		to, err := parseDay(batch.To)
		if err != nil {
			return err
		}
		if to.Before(from) || to.Sub(from) > maxBatchDays*24*time.Hour {
			return fmt.Errorf("a batch covers 1 to %d days", maxBatchDays)
		}
	}
	if batch.FinalThrough != "" {
		if _, err := parseDay(batch.FinalThrough); err != nil {
			return err
		}
	}
	for _, row := range batch.Rows {
		if _, err := parseDay(row.Day); err != nil {
			return err
		}
		if !currencyPattern.MatchString(row.Currency) {
			return fmt.Errorf("%q isn't a currency code", row.Currency)
		}
		if len(row.Tags) > 20 {
			return errors.New("too many tags on a row")
		}
	}
	return nil
}

func clip(value string) string {
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func clipLong(value string) string {
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

// dropStaleChunks removes chunks of batches that never completed.
func dropStaleChunks(ctx context.Context, now time.Time) error {
	return storage.GetDb().WithContext(ctx).
		Exec(`DELETE FROM cloud_cost_batches WHERE received_at < ?`, now.Add(-stagedFor)).Error
}
