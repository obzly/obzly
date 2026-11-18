package alerting

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"obzly/internal/storage"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// incidentQuietPeriod: an incident ends when no session sharing one of
	// its services started for this long; a session within it joins it.
	incidentQuietPeriod = 15 * time.Minute
	// keepEndedIncidents: how long ended incidents' notification rows are kept.
	keepEndedIncidents = 30 * 24 * time.Hour
	endIncidentsBatch  = 500

	defaultIncidentMinSignals  = 3
	defaultIncidentMinServices = 2

	// incidentAlertName is incident notifications' alertname label, which a
	// silence can match.
	incidentAlertName = "Incident"
)

// IncidentNotificationSettings is a project's "Notify when an incident
// opens/ends": an incident is notified once it holds MinSignals error
// signals or MinServices services.
type IncidentNotificationSettings struct {
	ProjectID   uuid.UUID  `json:"projectId"   gorm:"column:project_id;primaryKey"`
	NotifyOpen  bool       `json:"notifyOpen"  gorm:"column:notify_open"`
	NotifyEnd   bool       `json:"notifyEnd"   gorm:"column:notify_end"`
	MinSignals  int        `json:"minSignals"  gorm:"column:min_signals"`
	MinServices int        `json:"minServices" gorm:"column:min_services"`
	Severity    string     `json:"severity"    gorm:"column:severity"`
	ChannelIDs  UUIDList   `json:"channelIds"  gorm:"column:channel_ids"`
	UpdatedBy   *uuid.UUID `json:"-"           gorm:"column:updated_by"`
	UpdatedAt   *time.Time `json:"updatedAt"   gorm:"column:updated_at"`
}

func (IncidentNotificationSettings) TableName() string { return "incident_notification_settings" }

// IncidentNotificationRequest replaces a project's settings.
type IncidentNotificationRequest struct {
	NotifyOpen  bool        `json:"notifyOpen"`
	NotifyEnd   bool        `json:"notifyEnd"`
	MinSignals  int         `json:"minSignals"`
	MinServices int         `json:"minServices"`
	Severity    string      `json:"severity"`
	ChannelIDs  []uuid.UUID `json:"channelIds"`
}

// UUIDList is a JSON array of ids.
type UUIDList []uuid.UUID

func (l UUIDList) Value() (driver.Value, error) {
	if l == nil {
		l = UUIDList{}
	}
	return jsonValue(l)
}
func (l *UUIDList) Scan(value any) error { return scanJSON(value, l) }

// ObservedSession is a correlation session the scanner just stored.
type ObservedSession struct {
	ID          uuid.UUID
	Services    []string
	ErrorLogs   int
	ErrorSpans  int
	WindowStart time.Time
	WindowEnd   time.Time
}

// IncidentImpactSource ranks a session's services by error signals (the
// Incidents page's impact query).
type IncidentImpactSource func(projectID, sessionID uuid.UUID) ([]ImpactedService, error)

var incidentImpact IncidentImpactSource

// SetIncidentImpactSource wires the impact query (main.go; avoids an import cycle).
func SetIncidentImpactSource(source IncidentImpactSource) { incidentImpact = source }

// incidentRecord is one notified incident: the sessions the scanner keeps
// creating while it lasts are folded into the first one's row, so it opens
// and ends once. QualifiedAt is when it crossed the thresholds.
type incidentRecord struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	ProjectID   uuid.UUID  `gorm:"column:project_id"`
	SessionID   uuid.UUID  `gorm:"column:session_id"`
	Services    ProjectIDs `gorm:"column:services"`
	SignalCount int        `gorm:"column:signal_count"`
	ErrorLogs   int        `gorm:"column:error_logs"`
	ErrorSpans  int        `gorm:"column:error_spans"`
	TopServices impactList `gorm:"column:top_services"`
	Severity    string     `gorm:"column:severity"`
	OpenedAt    time.Time  `gorm:"column:opened_at"`
	LastSeenAt  time.Time  `gorm:"column:last_seen_at"`
	EndedAt     *time.Time `gorm:"column:ended_at"`
	QualifiedAt *time.Time `gorm:"column:notified_open_at"`
	NotifiedEnd *time.Time `gorm:"column:notified_end_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
}

func (incidentRecord) TableName() string { return "incident_notifications" }

type impactList []ImpactedService

func (l impactList) Value() (driver.Value, error) {
	if l == nil {
		l = impactList{}
	}
	return jsonValue(l)
}
func (l *impactList) Scan(value any) error { return scanJSON(value, l) }

// GetIncidentNotifications is a project's settings, or the defaults (off).
func (s *Service) GetIncidentNotifications(projectID uuid.UUID) (*IncidentNotificationSettings, error) {
	settings, err := incidentSettings(storage.GetDb(), projectID)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		settings = &IncidentNotificationSettings{
			ProjectID: projectID, MinSignals: defaultIncidentMinSignals, MinServices: defaultIncidentMinServices,
			Severity: SeverityHigh, ChannelIDs: UUIDList{},
		}
	}
	return settings, nil
}

// SaveIncidentNotifications replaces a project's settings (owners and admins).
func (s *Service) SaveIncidentNotifications(
	projectID uuid.UUID, request IncidentNotificationRequest, userID uuid.UUID,
) (*IncidentNotificationSettings, error) {
	request.Severity = strings.ToLower(strings.TrimSpace(request.Severity))
	if request.Severity == "" {
		request.Severity = SeverityHigh
	}
	switch {
	case !ValidSeverity(request.Severity):
		return nil, errors.New("severity must be critical, high, medium or low")
	case request.MinSignals < 1 || request.MinSignals > 100000:
		return nil, errors.New("the minimum signals must be between 1 and 100000")
	case request.MinServices < 1 || request.MinServices > 1000:
		return nil, errors.New("the minimum services must be between 1 and 1000")
	case len(request.ChannelIDs) > 20:
		return nil, errors.New("at most 20 channels")
	}
	channels, err := usableChannelIDs(storage.GetDb(), projectID, request.ChannelIDs)
	if err != nil {
		return nil, err
	}
	if len(channels) != len(uniqueIDs(request.ChannelIDs)) {
		return nil, errors.New("every channel must be available to this project")
	}
	if (request.NotifyOpen || request.NotifyEnd) && len(channels) == 0 {
		return nil, errors.New("choose at least one channel to notify")
	}
	now := time.Now().UTC()
	settings := &IncidentNotificationSettings{
		ProjectID: projectID, NotifyOpen: request.NotifyOpen, NotifyEnd: request.NotifyEnd,
		MinSignals: request.MinSignals, MinServices: request.MinServices, Severity: request.Severity,
		ChannelIDs: channels, UpdatedBy: &userID, UpdatedAt: &now,
	}
	if err := storage.GetDb().Save(settings).Error; err != nil {
		return nil, err
	}
	state := "off"
	if request.NotifyOpen || request.NotifyEnd {
		state = fmt.Sprintf("on (open %t, end %t, %d signals or %d services, %s, %d channels)", request.NotifyOpen,
			request.NotifyEnd, request.MinSignals, request.MinServices, request.Severity, len(channels))
	}
	s.audit.WriteAuditLog("Incident notifications "+state, &userID, &projectID)
	return settings, nil
}

// ObserveIncidentSessions folds the sessions the correlation scanner just
// stored for projectID into the project's incidents and queues the opening
// notification of each that crosses its thresholds, once. A project without
// incident notifications is skipped.
func ObserveIncidentSessions(projectID uuid.UUID, sessions []ObservedSession, now time.Time) error {
	if len(sessions) == 0 {
		return nil
	}
	settings, err := incidentSettings(storage.GetDb(), projectID)
	if err != nil || settings == nil || (!settings.NotifyOpen && !settings.NotifyEnd) {
		return err
	}
	sorted := append([]ObservedSession(nil), sessions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].WindowStart.Before(sorted[j].WindowStart) })
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var open []*incidentRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND ended_at IS NULL", projectID).Find(&open).Error; err != nil {
			return err
		}
		for _, session := range sorted {
			record := matchingIncident(open, session)
			if record == nil {
				record = &incidentRecord{
					ID: uuid.New(), ProjectID: projectID, SessionID: session.ID, Services: ProjectIDs{},
					TopServices: impactList{}, Severity: settings.Severity, OpenedAt: session.WindowStart,
					LastSeenAt: session.WindowEnd, CreatedAt: now,
				}
				open = append(open, record)
			}
			for _, service := range session.Services {
				if !slices.Contains(record.Services, service) {
					record.Services = append(record.Services, service)
				}
			}
			sort.Strings(record.Services)
			record.ErrorLogs += session.ErrorLogs
			record.ErrorSpans += session.ErrorSpans
			record.SignalCount = record.ErrorLogs + record.ErrorSpans
			if session.WindowEnd.After(record.LastSeenAt) {
				record.LastSeenAt = session.WindowEnd
			}
			if record.QualifiedAt == nil && crossesThresholds(settings, record) {
				record.QualifiedAt = &now
				record.TopServices = topServices(projectID, session.ID)
				if settings.NotifyOpen {
					if err := enqueueIncident(tx, settings, record, StatusFiring, now); err != nil {
						return err
					}
				}
			}
			if err := tx.Save(record).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// EndQuietIncidents ends the incidents no session joined for the quiet
// period, queues the ending notification of those that were notified (when
// their project asks for it), and prunes old ended ones.
func EndQuietIncidents(now time.Time) error {
	var quiet []*incidentRecord
	if err := storage.GetDb().Where("ended_at IS NULL AND last_seen_at < ?", now.Add(-incidentQuietPeriod)).
		Order("last_seen_at").Limit(endIncidentsBatch).Find(&quiet).Error; err != nil {
		return err
	}
	for _, record := range quiet {
		err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
			// Ended once, whichever replica gets here first.
			result := tx.Model(&incidentRecord{}).Where("id = ? AND ended_at IS NULL", record.ID).
				Update("ended_at", now)
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			record.EndedAt = &now
			if record.QualifiedAt == nil {
				return nil
			}
			settings, err := incidentSettings(tx, record.ProjectID)
			if err != nil || settings == nil || !settings.NotifyEnd {
				return err
			}
			if err := enqueueIncident(tx, settings, record, StatusResolved, now); err != nil {
				return err
			}
			return tx.Model(&incidentRecord{}).Where("id = ?", record.ID).Update("notified_end_at", now).Error
		})
		if err != nil {
			return err
		}
	}
	return storage.GetDb().Exec(
		`DELETE FROM incident_notifications WHERE id IN (
			SELECT id FROM incident_notifications WHERE ended_at < ? LIMIT 1000)`,
		now.Add(-keepEndedIncidents)).Error
}

func incidentSettings(tx *gorm.DB, projectID uuid.UUID) (*IncidentNotificationSettings, error) {
	var settings IncidentNotificationSettings
	err := tx.Where("project_id = ?", projectID).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &settings, err
}

// matchingIncident is the open incident session joins: one sharing a
// service that it started within the quiet period of.
func matchingIncident(open []*incidentRecord, session ObservedSession) *incidentRecord {
	for _, record := range open {
		if session.WindowStart.After(record.LastSeenAt.Add(incidentQuietPeriod)) {
			continue
		}
		for _, service := range session.Services {
			if slices.Contains(record.Services, service) {
				return record
			}
		}
	}
	return nil
}

func crossesThresholds(settings *IncidentNotificationSettings, record *incidentRecord) bool {
	return record.SignalCount >= settings.MinSignals || len(record.Services) >= settings.MinServices
}

// topServices is the session's top impacted services (at most 5); none when
// the impact query fails, which never holds the notification back.
func topServices(projectID, sessionID uuid.UUID) impactList {
	if incidentImpact == nil {
		return impactList{}
	}
	services, err := incidentImpact(projectID, sessionID)
	if err != nil {
		logger.GetLogger().Warn("incident notification: impact query failed", "sessionId", sessionID, "error", err)
		return impactList{}
	}
	if len(services) > 5 {
		services = services[:5]
	}
	return services
}

func enqueueIncident(
	tx *gorm.DB, settings *IncidentNotificationSettings, record *incidentRecord, status string, now time.Time,
) error {
	channels, err := usableChannelIDs(tx, record.ProjectID, settings.ChannelIDs)
	if err != nil || len(channels) == 0 {
		return err
	}
	n := incidentNotification(record, status)
	for _, channelID := range channels {
		item := &OutboxItem{
			ID: uuid.New(), ProjectID: record.ProjectID, ChannelID: channelID, Fingerprint: n.Fingerprint,
			Status: status, Payload: n, NextAttemptAt: now, CreatedAt: now,
		}
		if err := tx.Create(item).Error; err != nil {
			return err
		}
	}
	return nil
}

func incidentNotification(record *incidentRecord, status string) Notification {
	services := []string(record.Services)
	names := services
	if len(names) > 3 {
		names = append(append([]string{}, names[:3]...), fmt.Sprintf("+%d more", len(services)-3))
	}
	n := Notification{
		Kind: KindIncident, Status: status, Severity: normalizeSeverity(record.Severity),
		ProjectID: record.ProjectID, RuleName: "Incident: " + strings.Join(names, ", "), Source: KindIncident,
		Summary: fmt.Sprintf("%d error signal(s) across %d service(s)", record.SignalCount, len(services)),
		Labels: Labels{"alertname": incidentAlertName, "obzly_kind": KindIncident,
			"severity": normalizeSeverity(record.Severity)},
		Fingerprint: "incident:" + record.ID.String(), StartsAt: record.OpenedAt,
		Incident: &IncidentDetails{
			ID: record.SessionID, Services: services, SignalCount: record.SignalCount,
			ErrorLogs: record.ErrorLogs, ErrorSpans: record.ErrorSpans, TopServices: record.TopServices,
		},
	}
	if status == StatusResolved {
		ended := record.LastSeenAt
		n.EndsAt = &ended
	}
	return n
}
