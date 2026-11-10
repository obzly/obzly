package posture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

const (
	maxBatchChunks = 200
	// staleChunksAfter: chunks of a batch that never completed are dropped after this.
	staleChunksAfter = 24 * time.Hour
	maxTextChars     = 1000
	maxEvidence      = 20
)

// Receive stages one chunk of a batch and applies the batch once all chunks have arrived. It
// reports whether this chunk completed it.
func (s *Service) Receive(ctx context.Context, projectID uuid.UUID, batch spec.PostureBatch) (bool, error) {
	if err := validateBatch(batch); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidBatch, err)
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return false, err
	}
	db := storage.GetDb().WithContext(ctx)
	err = db.Exec(`INSERT INTO posture_batches (batch_id, seq, project_id, total, payload) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (batch_id, seq) DO NOTHING`, batch.BatchID, batch.Seq, projectID, batch.Total, string(payload)).Error
	if err != nil {
		return false, err
	}
	var received int64
	if err := db.Raw(`SELECT count(*) FROM posture_batches WHERE batch_id = ? AND project_id = ?`,
		batch.BatchID, projectID).Scan(&received).Error; err != nil {
		return false, err
	}
	if received < int64(batch.Total) {
		return false, nil
	}
	return s.apply(ctx, projectID, batch.BatchID)
}

// apply merges a complete batch: its runs replace their scopes' runs, its findings open or
// reopen, and open findings it no longer has resolve where an ok run read their scope again.
func (s *Service) apply(ctx context.Context, projectID uuid.UUID, batchID string) (bool, error) {
	applied := false
	err := storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "cloud-posture:"+batchID).Error; err != nil {
			return err
		}
		var payloads []string
		if err := tx.Raw(`SELECT payload FROM posture_batches WHERE batch_id = ? AND project_id = ? ORDER BY seq`,
			batchID, projectID).Scan(&payloads).Error; err != nil {
			return err
		}
		if len(payloads) == 0 {
			return nil
		}
		var merged spec.PostureBatch
		for _, payload := range payloads {
			var chunk spec.PostureBatch
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return err
			}
			if merged.BatchID == "" {
				merged = chunk
				continue
			}
			merged.Runs = append(merged.Runs, chunk.Runs...)
			merged.Findings = append(merged.Findings, chunk.Findings...)
		}
		connectionID := uuid.MustParse(merged.ConnectionID)
		// One connection's batches apply one at a time, in order.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`,
			"cloud-posture:"+projectID.String()+":"+connectionID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM posture_batches WHERE batch_id = ? AND project_id = ?`, batchID,
			projectID).Error; err != nil {
			return err
		}
		applied = true
		return s.merge(tx, projectID, connectionID, merged)
	})
	return applied, err
}

func (s *Service) merge(tx *gorm.DB, projectID, connectionID uuid.UUID, batch spec.PostureBatch) error {
	now := s.now()
	scanned := batch.ScannedAt.UTC()
	if scanned.IsZero() || scanned.After(now) {
		scanned = now
	}
	runs := make([]scopeRun, 0, len(batch.Runs))
	for _, run := range batch.Runs {
		runs = append(runs, scopeRun{
			ProjectID: projectID, ConnectionID: connectionID, ConnectionName: batch.ConnectionName,
			Provider: batch.Provider, Account: run.Account, Region: run.Region, Standard: run.Standard,
			Source: run.Source, Result: run.Result, Reason: clipText(run.Reason, maxReasonChars),
			Passed: orEmpty(run.Passed), Failed: orEmpty(run.Failed), ScannedAt: scanned,
		})
	}
	if len(runs) > 0 {
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(runs, 500).Error; err != nil {
			return err
		}
	}

	var existing []Finding
	if err := tx.Where("project_id = ? AND connection_id = ?", projectID, connectionID).
		Find(&existing).
		Error; err != nil {
		return err
	}
	byKey := make(map[string]*Finding, len(existing))
	for i := range existing {
		byKey[findingKey(existing[i].Control, existing[i].ResourceKey)] = &existing[i]
	}
	seen := map[string]bool{}
	var events []Event
	for _, incoming := range batch.Findings {
		key := findingKey(incoming.Control, incoming.ResourceKey)
		if seen[key] {
			continue
		}
		seen[key] = true
		finding := byKey[key]
		if finding == nil {
			finding = &Finding{ID: uuid.New(), ProjectID: projectID, ConnectionID: connectionID, FirstSeen: scanned,
				Status: StatusOpen}
			fill(finding, batch, incoming, scanned)
			if err := tx.Create(finding).Error; err != nil {
				return err
			}
			events = append(events, event(finding, EventOpened, scanned, "", ""))
			continue
		}
		reopened := finding.Status == StatusResolved
		fill(finding, batch, incoming, scanned)
		if reopened {
			finding.Status, finding.ResolvedAt = StatusOpen, nil
			events = append(events, event(finding, EventReopened, scanned, "", ""))
		}
		if err := tx.Save(finding).Error; err != nil {
			return err
		}
	}

	resolvers := okRuns(runs)
	for i := range existing {
		finding := &existing[i]
		if finding.Status != StatusOpen || seen[findingKey(finding.Control, finding.ResourceKey)] ||
			!resolves(resolvers, finding) {
			continue
		}
		resolvedAt := scanned
		if err := tx.Model(&Finding{}).Where("id = ?", finding.ID).
			Updates(map[string]any{"status": StatusResolved, "resolved_at": resolvedAt}).Error; err != nil {
			return err
		}
		events = append(events, event(finding, EventResolved, scanned, "", ""))
	}
	if len(events) > 0 {
		if err := tx.CreateInBatches(events, 500).Error; err != nil {
			return err
		}
	}
	return snapshotScores(tx, projectID, connectionID, scanned)
}

// okRuns indexes the batch's ok runs by scope (account, region).
func okRuns(runs []scopeRun) map[string][]scopeRun {
	byScope := map[string][]scopeRun{}
	for _, run := range runs {
		if run.Result == spec.PostureRunOK {
			key := run.Account + "|" + run.Region
			byScope[key] = append(byScope[key], run)
		}
	}
	return byScope
}

// resolves: a finding missing from a batch resolves only when an ok run read its scope again,
// from its own source (it's gone there) or from one that evaluated its control. A source that
// doesn't evaluate the control, or a scope that wasn't read, leaves it open: unknown isn't a pass.
func resolves(byScope map[string][]scopeRun, finding *Finding) bool {
	for _, run := range byScope[finding.Account+"|"+finding.Region] {
		if run.Source == finding.Source {
			return true
		}
		for _, ref := range finding.Standards {
			if ref.Standard == run.Standard &&
				(slices.Contains(run.Passed, ref.Control) || slices.Contains(run.Failed, ref.Control)) {
				return true
			}
		}
	}
	return false
}

// snapshotScores records each standard's score for the connection today, for Trends.
func snapshotScores(tx *gorm.DB, projectID, connectionID uuid.UUID, at time.Time) error {
	scores, err := standardScores(tx, []uuid.UUID{projectID}, &connectionID)
	if err != nil {
		return err
	}
	day := at.UTC().Format("2006-01-02")
	for _, score := range scores {
		err := tx.Exec(`INSERT INTO posture_scores_daily (project_id, connection_id, standard, day, passed, failed)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, connection_id, standard, day)
			DO UPDATE SET passed = EXCLUDED.passed, failed = EXCLUDED.failed`,
			projectID, connectionID, score.Standard, day, score.Passed, score.Failed).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func fill(finding *Finding, batch spec.PostureBatch, incoming spec.PostureFinding, seen time.Time) {
	finding.ConnectionName, finding.Provider = batch.ConnectionName, batch.Provider
	finding.Account, finding.Region, finding.Source = incoming.Account, incoming.Region, incoming.Source
	finding.Control, finding.CheckID = incoming.Control, incoming.Check
	finding.Title = clipText(incoming.Title, maxTextChars)
	finding.Severity = incoming.Severity
	if _, ok := severityRank[finding.Severity]; !ok {
		finding.Severity = spec.SeverityMedium
	}
	finding.ResourceKey, finding.ResourceType = incoming.ResourceKey, clipText(incoming.ResourceType, 200)
	finding.Standards = incoming.Standards
	if finding.Standards == nil {
		finding.Standards = standardRefs{}
	}
	finding.Evidence = boundedEvidence(incoming.Evidence)
	finding.RemediationURL = clipText(incoming.RemediationURL, maxTextChars)
	finding.LastSeen = seen
}

func event(finding *Finding, kind string, at time.Time, by, note string) Event {
	return Event{FindingID: finding.ID, ProjectID: finding.ProjectID, Kind: kind, Severity: finding.Severity, At: at,
		ByUser: by, Note: note}
}

func validateBatch(batch spec.PostureBatch) error {
	if batch.BatchID == "" || len(batch.BatchID) > 64 {
		return errors.New("no batch ID")
	}
	if batch.Total < 1 || batch.Total > maxBatchChunks || batch.Seq < 1 || batch.Seq > batch.Total {
		return errors.New("bad chunk numbering")
	}
	if _, err := uuid.Parse(batch.ConnectionID); err != nil {
		return errors.New("the connection ID isn't a UUID")
	}
	if batch.Provider != spec.ProviderAWS && batch.Provider != spec.ProviderAzure {
		return errors.New("provider must be aws or azure")
	}
	sources := []string{spec.PostureSourceNative, spec.PostureSourceBaseline}
	results := []string{spec.PostureRunOK, spec.PostureRunNotEnabled, spec.PostureRunNeedsPermission,
		spec.PostureRunError}
	for _, run := range batch.Runs {
		if run.Account == "" || run.Region == "" || run.Standard == "" || !slices.Contains(sources, run.Source) ||
			!slices.Contains(results, run.Result) {
			return fmt.Errorf("a run needs an account, region, standard, source and result (%+v)", run)
		}
	}
	for _, finding := range batch.Findings {
		if finding.Control == "" || finding.ResourceKey == "" || finding.Account == "" || finding.Region == "" ||
			!slices.Contains(sources, finding.Source) {
			return errors.New("a finding needs a control, resource, account, region and source")
		}
		if len(finding.Control) > 200 || len(finding.ResourceKey) > 2048 {
			return errors.New("a finding's control or resource is too long")
		}
	}
	return nil
}

// dropStaleChunks removes chunks of batches that never completed.
func dropStaleChunks(ctx context.Context, now time.Time) error {
	return storage.GetDb().WithContext(ctx).
		Exec(`DELETE FROM posture_batches WHERE received_at < ?`, now.Add(-staleChunksAfter)).Error
}

func findingKey(control, resource string) string { return control + "\x00" + resource }

func boundedEvidence(evidence map[string]string) stringMap {
	out := stringMap{}
	for key, value := range evidence {
		if len(out) >= maxEvidence {
			break
		}
		out[clipText(key, 100)] = clipText(value, maxReasonChars)
	}
	return out
}

func clipText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

func orEmpty(values []string) stringSlice {
	if values == nil {
		return stringSlice{}
	}
	return values
}
