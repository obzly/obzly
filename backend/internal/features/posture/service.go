package posture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"obzly/internal/cache"
	"obzly/internal/features/alerting"
	"obzly/internal/features/audit_logs"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
	"obzly/internal/util/leaderlock"
)

const maintenanceEvery = time.Hour

type Service struct {
	log *slog.Logger
	now func() time.Time
}

// FindingUpdate suppresses a finding (with a reason, until a time or for good) or lifts it.
type FindingUpdate struct {
	Suppressed bool   `json:"suppressed"`
	Reason     string `json:"reason"`
	// Until: when the suppression ends and the finding counts again (RFC 3339); empty is never.
	Until string `json:"until,omitempty"`
}

// AlertSettingsDTO: a project's channels for new and reopened findings this severe or more.
type AlertSettingsDTO struct {
	ChannelIDs  []uuid.UUID `json:"channelIds"`
	MinSeverity string      `json:"minSeverity"`
}

// UpdateFinding suppresses or unsuppresses a finding; both go into its history and the audit log.
func (s *Service) UpdateFinding(
	ctx context.Context, user *users_models.User, id uuid.UUID, update FindingUpdate,
) (*Finding, error) {
	now := s.now()
	reason := strings.TrimSpace(update.Reason)
	var until *time.Time
	if update.Suppressed {
		if reason == "" || len(reason) > maxNoteChars {
			return nil, fmt.Errorf("give a reason of up to %d characters for suppressing it", maxNoteChars)
		}
		if update.Until != "" {
			parsed, err := time.Parse(time.RFC3339, update.Until)
			if err != nil || !parsed.After(now) {
				return nil, errors.New("a suppression ends at a future time, like 2027-01-31T00:00:00Z")
			}
			parsed = parsed.UTC()
			until = &parsed
		}
	}
	var finding Finding
	err := storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&finding).Error; err != nil {
			return ErrNotFound
		}
		by := user.Email
		kind, note := EventSuppressed, reason
		if update.Suppressed {
			finding.Suppressed, finding.SuppressReason, finding.SuppressedTill, finding.SuppressedBy = true, reason, until,
				by
			if until != nil {
				note = fmt.Sprintf("%s (until %s)", reason, until.Format(dayLayout))
			}
		} else {
			if !finding.Suppressed {
				return nil
			}
			kind, note = EventUnsuppressed, reason
			finding.Suppressed, finding.SuppressReason, finding.SuppressedTill, finding.SuppressedBy = false, "", nil, ""
		}
		if err := tx.Save(&finding).Error; err != nil {
			return err
		}
		return tx.Create(&Event{FindingID: finding.ID, ProjectID: finding.ProjectID, Kind: kind,
			Severity: finding.Severity, At: now, ByUser: by, Note: note}).Error
	})
	if err != nil {
		return nil, err
	}
	verb := "Unsuppressed"
	if update.Suppressed {
		verb = "Suppressed"
	}
	audit_logs.GetAuditLogService().WriteAuditLog(
		fmt.Sprintf("%s posture finding %s on %s: %s", verb, finding.Control, finding.ResourceKey, reason),
		&user.ID, &finding.ProjectID)
	return &finding, nil
}

func (s *Service) findingProject(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	var finding Finding
	if err := storage.GetDb().
		WithContext(ctx).
		Select("project_id").
		Where("id = ?", id).
		First(&finding).
		Error; err != nil {
		return uuid.Nil, ErrNotFound
	}
	return finding.ProjectID, nil
}

func (s *Service) AlertSettings(ctx context.Context, projectID uuid.UUID) (*AlertSettingsDTO, error) {
	var rows []alertSettings
	if err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &AlertSettingsDTO{ChannelIDs: []uuid.UUID{}, MinSeverity: spec.SeverityHigh}, nil
	}
	return &AlertSettingsDTO{ChannelIDs: append([]uuid.UUID{}, rows[0].ChannelIDs...),
		MinSeverity: rows[0].MinSeverity}, nil
}

// UpdateAlertSettings keeps the project's finding alert rule: none without channels.
func (s *Service) UpdateAlertSettings(
	ctx context.Context, projectID uuid.UUID, request AlertSettingsDTO,
) (*AlertSettingsDTO, error) {
	if request.MinSeverity == "" {
		request.MinSeverity = spec.SeverityHigh
	}
	if _, ok := severityRank[request.MinSeverity]; !ok || request.MinSeverity == spec.SeverityInformational {
		return nil, errors.New("alert on critical, high, medium or low findings and more severe")
	}
	channels, err := alerting.UsableChannelIDs(projectID, request.ChannelIDs)
	if err != nil {
		return nil, err
	}
	row := alertSettings{ProjectID: projectID, ChannelIDs: channels, MinSeverity: request.MinSeverity,
		UpdatedAt: s.now()}
	err = storage.GetDb().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_ids", "min_severity", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	key := alerting.ManagedByCloudPosturePrefix + "findings:" + projectID.String()
	if len(channels) == 0 {
		if err := alerting.DeleteManagedRule(key); err != nil {
			return nil, err
		}
	} else if err := alerting.SaveManagedRule(&alerting.ManagedRule{
		Key: key, ProjectID: projectID, Name: "Cloud posture finding", Source: alerting.SourceCloudPosture,
		Query: alerting.RuleQuery{MinSeverity: request.MinSeverity}, Comparison: ">", Threshold: 0,
		IntervalSeconds: alertInterval, Labels: alerting.Labels{"source": "posture"},
		Summary: "{{ $labels.severity }} posture finding {{ $labels.control }} on {{ $labels.resource }}: " +
			"{{ $labels.title }}",
		ChannelIDs: channels,
	}); err != nil {
		return nil, err
	}
	return &AlertSettingsDTO{ChannelIDs: append([]uuid.UUID{}, channels...), MinSeverity: request.MinSeverity}, nil
}

// EvaluateRule is the cloud_posture alert source: a series per finding opened or reopened lately
// that is still open, unsuppressed and severe enough.
func (s *Service) EvaluateRule(
	ctx context.Context,
	rule *alerting.AlertRule,
	now time.Time,
) ([]alerting.SourceSample, error) {
	minimum, ok := severityRank[rule.Query.MinSeverity]
	if !ok {
		minimum = severityRank[spec.SeverityHigh]
	}
	var severities []string
	for severity, rank := range severityRank {
		if rank <= minimum {
			severities = append(severities, severity)
		}
	}
	var findings []Finding
	err := storage.GetDb().WithContext(ctx).Where(`project_id = ? AND status = 'open' AND NOT suppressed
		AND severity IN ? AND id IN (SELECT finding_id FROM posture_finding_events
			WHERE project_id = ? AND kind IN ('opened', 'reopened') AND at > ?)`,
		rule.ProjectID, severities, rule.ProjectID, now.Add(-alertLookback)).
		Order(severityOrder).Limit(200).Find(&findings).Error
	if err != nil {
		return nil, err
	}
	samples := make([]alerting.SourceSample, 0, len(findings))
	for _, finding := range findings {
		samples = append(samples, alerting.SourceSample{Value: 1, Labels: alerting.Labels{
			"severity": finding.Severity, "control": finding.Control, "resource": finding.ResourceKey,
			"title": finding.Title, "account": finding.Account, "region": finding.Region,
		}})
	}
	return samples, nil
}

// projectsFor is the projects user can read, narrowed to projectID when one is given.
func (s *Service) projectsFor(user *users_models.User, projectID string) ([]uuid.UUID, error) {
	list, err := projects_services.GetProjectService().GetUserProjects(user)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for _, project := range list.Projects {
		if projectID != "" && project.ID.String() != strings.TrimSpace(projectID) {
			continue
		}
		ids = append(ids, project.ID)
	}
	if projectID != "" && len(ids) == 0 {
		return nil, ErrForbidden
	}
	return ids, nil
}

// StartMaintenance lifts expired suppressions and drops chunks of batches that never completed,
// hourly (leader-locked).
func (s *Service) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(maintenanceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := leaderlock.RunExclusive(ctx, cache.GetCache(), "cloud-posture-maintenance",
					2*maintenanceEvery, func(ctx context.Context) error { return s.maintain(ctx) })
				if err != nil {
					s.log.Warn("Cloud posture maintenance failed", "error", err)
				}
			}
		}
	}()
}

func (s *Service) maintain(ctx context.Context) error {
	now := s.now()
	if err := dropStaleChunks(ctx, now); err != nil {
		return err
	}
	return expireSuppressions(ctx, now)
}

// expireSuppressions lifts suppressions whose time is up, so the findings count again.
func expireSuppressions(ctx context.Context, now time.Time) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var expired []Finding
		if err := tx.Where("suppressed AND suppressed_until IS NOT NULL AND suppressed_until <= ?", now).
			Find(&expired).Error; err != nil || len(expired) == 0 {
			return err
		}
		events := make([]Event, 0, len(expired))
		ids := make([]uuid.UUID, 0, len(expired))
		for i := range expired {
			ids = append(ids, expired[i].ID)
			events = append(events, event(&expired[i], EventUnsuppressed, now, "", "suppression expired"))
		}
		if err := tx.Model(&Finding{}).Where("id IN ?", ids).Updates(map[string]any{
			"suppressed": false, "suppress_reason": "", "suppressed_until": nil, "suppressed_by": "",
		}).Error; err != nil {
			return err
		}
		return tx.CreateInBatches(events, 500).Error
	})
}
