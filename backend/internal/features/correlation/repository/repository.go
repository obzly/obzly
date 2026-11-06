package correlation_repository

import (
	"errors"
	"fmt"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	logs_core "obzly/internal/features/logs/core"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultSessionListLimit = 100
	maxSessionListLimit     = 500
)

// IncidentSessionRepository implements correlation_core.IncidentSessionRepository.
type IncidentSessionRepository struct{}

func New() *IncidentSessionRepository {
	return &IncidentSessionRepository{}
}

func (r *IncidentSessionRepository) CreateSession(
	session *correlation_core.IncidentSession,
	signals []*correlation_core.IncidentSessionSignal,
) error {
	if len(signals) == 0 {
		return errors.New("correlation_repository: a session must have at least one signal")
	}

	if session.ID == uuid.Nil {
		session.ID = uuid.New()
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}

	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		row := sessionRowFromModel(session)
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("failed to create incident session: %w", err)
		}

		signalRows := make([]signalRow, 0, len(signals))
		for _, signal := range signals {
			if signal.ID == uuid.Nil {
				signal.ID = uuid.New()
			}
			signal.SessionID = session.ID
			signalRows = append(signalRows, signalRowFromModel(signal))
		}

		if err := tx.Create(&signalRows).Error; err != nil {
			return fmt.Errorf("failed to create incident session signals: %w", err)
		}

		return nil
	})
}

func (r *IncidentSessionRepository) ListSessions(
	projectID uuid.UUID,
	request *correlation_core.ListSessionsRequestDTO,
) (*correlation_core.ListSessionsResponseDTO, error) {
	limit := defaultSessionListLimit
	if request != nil && request.Limit > 0 {
		limit = request.Limit
	}
	if limit > maxSessionListLimit {
		limit = maxSessionListLimit
	}

	query := storage.GetDb().
		Where("project_id = ?", projectID).
		Order("window_start DESC").
		Limit(limit)

	if request != nil && request.TimeRange != nil {
		if request.TimeRange.From != nil {
			query = query.Where("window_end >= ?", *request.TimeRange.From)
		}
		if request.TimeRange.To != nil {
			query = query.Where("window_start <= ?", *request.TimeRange.To)
		}
	}

	var rows []sessionRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list incident sessions: %w", err)
	}

	sessions := make([]correlation_core.SessionSummaryDTO, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, row.toModel().ToSummaryDTO())
	}

	return &correlation_core.ListSessionsResponseDTO{Sessions: sessions}, nil
}

func (r *IncidentSessionRepository) GetSessionDetail(
	projectID, sessionID uuid.UUID,
) (*correlation_core.SessionDetailDTO, error) {
	var sessionRow_ sessionRow
	err := storage.GetDb().
		Where("id = ? AND project_id = ?", sessionID, projectID).
		First(&sessionRow_).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load incident session: %w", err)
	}

	var signalRows []signalRow
	err = storage.GetDb().
		Where("session_id = ?", sessionID).
		Order("occurred_at ASC").
		Find(&signalRows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load incident session signals: %w", err)
	}

	signals := make([]correlation_core.SignalDetailDTO, 0, len(signalRows))
	for _, row := range signalRows {
		signals = append(signals, rehydrateSignal(projectID, row))
	}

	return &correlation_core.SessionDetailDTO{
		Session: sessionRow_.toModel().ToSummaryDTO(),
		Signals: signals,
	}, nil
}

// rehydrateSignal is best-effort: a row that is gone (e.g. retention) comes
// back with Available: false rather than failing the response.
func rehydrateSignal(projectID uuid.UUID, row signalRow) correlation_core.SignalDetailDTO {
	detail := correlation_core.SignalDetailDTO{
		SignalType: correlation_core.SignalType(row.SignalType),
		OccurredAt: row.OccurredAt,
	}

	switch correlation_core.SignalType(row.SignalType) {
	case correlation_core.SignalTypeLog:
		if content := rehydrateLog(projectID, row.SignalRef); content != nil {
			detail.Available = true
			detail.Log = content
		}
	case correlation_core.SignalTypeSpan:
		if content := rehydrateSpan(projectID, row.SignalRef); content != nil {
			detail.Available = true
			detail.Span = content
		}
	}

	return detail
}

func rehydrateLog(
	projectID uuid.UUID,
	ref correlation_core.JSONMap,
) *correlation_core.LogSignalContentDTO {
	logID, _ := ref["logId"].(string)
	if logID == "" {
		return nil
	}

	response, err := logs_core.GetLogCoreRepository().
		ExecuteQueryForProject(projectID, &logs_core.LogQueryRequestDTO{
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeCondition,
				Condition: &logs_core.ConditionNode{
					Field:    "id",
					Operator: logs_core.ConditionOperatorEquals,
					Value:    logID,
				},
			},
			Limit: 1,
		})
	if err != nil || len(response.Logs) == 0 {
		return nil
	}

	log := response.Logs[0]
	return &correlation_core.LogSignalContentDTO{
		Level:     log.Level,
		Message:   log.Message,
		Timestamp: log.Timestamp,
	}
}

func rehydrateSpan(
	projectID uuid.UUID,
	ref correlation_core.JSONMap,
) *correlation_core.SpanSignalContentDTO {
	traceID, _ := ref["traceId"].(string)
	spanID, _ := ref["spanId"].(string)
	if traceID == "" || spanID == "" {
		return nil
	}

	response, err := spans_core.GetSpanCoreRepository().GetTrace(projectID, traceID, nil)
	if err != nil || response == nil {
		return nil
	}

	for _, span := range response.Spans {
		if span.SpanID == spanID {
			return &correlation_core.SpanSignalContentDTO{
				Name:        span.Name,
				ServiceName: span.ServiceName,
				StatusCode:  span.StatusCode,
				TraceID:     span.TraceID,
				SpanID:      span.SpanID,
				StartTime:   span.StartTime,
			}
		}
	}
	return nil
}

// GetWatermark returns (zero time, nil) for a never-scanned project.
func (r *IncidentSessionRepository) GetWatermark(projectID uuid.UUID) (time.Time, error) {
	var row watermarkRow
	err := storage.GetDb().Where("project_id = ?", projectID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to load correlation watermark: %w", err)
	}
	return row.LastScannedAt, nil
}

func (r *IncidentSessionRepository) SetWatermark(projectID uuid.UUID, t time.Time) error {
	row := watermarkRow{ProjectID: projectID, LastScannedAt: t}
	err := storage.GetDb().
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_scanned_at"}),
		}).
		Create(&row).Error
	if err != nil {
		return fmt.Errorf("failed to set correlation watermark: %w", err)
	}
	return nil
}

func (r *IncidentSessionRepository) TestConnection() error {
	return storage.GetDb().Exec("SELECT 1").Error
}
