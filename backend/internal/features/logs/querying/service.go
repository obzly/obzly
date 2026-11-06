package logs_querying

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"obzly/internal/authz"
	"obzly/internal/config"
	"obzly/internal/features/disk"
	logs_core "obzly/internal/features/logs/core"
	projects_services "obzly/internal/features/projects/services"
	system_metrics "obzly/internal/features/system/metrics"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// ingestionRateWindowMinutes is the trailing window the System Dashboard
// averages ingestion over: smooths one slow minute, still feels current.
const ingestionRateWindowMinutes = 15

type LogQueryService struct {
	projectService         *projects_services.ProjectService
	concurrentQueryLimiter *ConcurrentQueryLimiter
	queryValidator         *QueryValidator
	logger                 *slog.Logger
}

func (s *LogQueryService) ExecuteQuery(
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	user *users_models.User,
) (*logs_core.LogQueryResponseDTO, error) {
	release, err := s.prepareQuery(projectID, request, user)
	if err != nil {
		return nil, err
	}
	defer release()

	return logs_core.GetLogCoreRepository().ExecuteQueryForProject(projectID, request)
}

// StreamQuery is ExecuteQuery that hands emit each search window's rows. start
// runs after every check passes, so those errors come back before any frame.
func (s *LogQueryService) StreamQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	user *users_models.User,
	start func(),
	emit logs_core.SearchEmitter,
) (*logs_core.LogQueryResponseDTO, error) {
	release, err := s.prepareQuery(projectID, request, user)
	if err != nil {
		return nil, err
	}
	defer release()

	start()
	return logs_core.GetLogCoreRepository().StreamQueryForProject(ctx, projectID, request, emit)
}

// prepareQuery takes a concurrency slot (release frees it), checks access
// and validates the request.
func (s *LogQueryService) prepareQuery(
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	user *users_models.User,
) (func(), error) {
	queryID := uuid.New().String()
	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	release := func() { s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID) }

	// Global admins can access any project, regular users only projects they're members
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		release()
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		release()
		return nil, errors.New("insufficient permissions to query project logs")
	}

	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		release()
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}

	if err := s.validateTimeRange(request.TimeRange); err != nil {
		release()
		return nil, err
	}

	if request.Cursor != "" {
		if _, err := logs_core.DecodeCursor(request.Cursor); err != nil {
			release()
			return nil, err
		}
	}

	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	return release, nil
}

// ExecutePatterns returns the most frequent message patterns among the logs a
// query matches, with ExecuteQuery's access, concurrency and validation checks.
func (s *LogQueryService) ExecutePatterns(
	projectID uuid.UUID,
	request *logs_core.PatternsRequestDTO,
	user *users_models.User,
) (*logs_core.PatternsResponseDTO, error) {
	queryID := uuid.New().String()

	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to query project logs")
	}

	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}

	if err := s.validateTimeRange(request.TimeRange); err != nil {
		return nil, err
	}

	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	return logs_core.GetLogCoreRepository().ExecutePatterns(projectID, request)
}

// ExecuteFieldValues returns a field's most frequent values among the logs a
// query matches, with the same checks as ExecutePatterns.
func (s *LogQueryService) ExecuteFieldValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.FieldValuesRequestDTO,
	user *users_models.User,
) (*logs_core.FieldValuesResponseDTO, error) {
	queryID := uuid.New().String()

	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		return nil, errors.New("insufficient permissions to query project logs")
	}

	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}

	if err := s.validateTimeRange(request.TimeRange); err != nil {
		return nil, err
	}

	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	if !request.IncludeRecent {
		return logs_core.GetLogCoreRepository().ExecuteFieldValues(ctx, projectID, request)
	}

	// Read alongside the facet query, so it adds no wait to it.
	recentDone := make(chan recentValues, 1)
	go func() {
		recent, err := readRecentValues(ctx, projectID, request, time.Now().UTC())
		if err != nil {
			// Recent values are a convenience: the facet answers without them.
			s.logger.Warn("Failed to read recent field values", slog.String("projectId", projectID.String()),
				slog.String("error", err.Error()))
		}
		recentDone <- recent
	}()
	response, err := logs_core.GetLogCoreRepository().ExecuteFieldValues(ctx, projectID, request)
	recent := <-recentDone
	if err != nil {
		return nil, err
	}
	addRecentValues(response, recent)
	return response, nil
}

// ExecutePatternChanges compares message patterns between two ranges, with
// the same checks as ExecuteFieldValues for both.
func (s *LogQueryService) ExecutePatternChanges(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.PatternChangesRequestDTO,
	user *users_models.User,
) (*logs_core.PatternChangesResponseDTO, error) {
	queryID := uuid.New().String()
	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		return nil, errors.New("insufficient permissions to query project logs")
	}
	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}
	for _, timeRange := range []*logs_core.TimeRangeDTO{request.Current, request.Baseline} {
		if err := s.validateTimeRange(timeRange); err != nil {
			return nil, err
		}
	}
	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	return logs_core.GetLogCoreRepository().ExecutePatternChanges(ctx, projectID, request)
}

// ExecuteDeploys returns the Deployment rollouts that began in the range.
func (s *LogQueryService) ExecuteDeploys(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.DeploysRequestDTO,
	user *users_models.User,
) (*logs_core.DeploysResponseDTO, error) {
	queryID := uuid.New().String()
	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		return nil, errors.New("insufficient permissions to query project logs")
	}
	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}
	if err := s.validateTimeRange(request.TimeRange); err != nil {
		return nil, err
	}
	return logs_core.GetLogCoreRepository().ExecuteDeploys(ctx, projectID, request)
}

// ExecuteContext returns the logs just before and after one log. An omitted
// scope is the anchor's workload plus LOG_CONTEXT_DEFAULT_FIELDS.
func (s *LogQueryService) ExecuteContext(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogContextRequestDTO,
	user *users_models.User,
) (*logs_core.LogContextResponseDTO, error) {
	invalid := func(message string) error {
		return &ValidationError{Code: logs_core.ErrorInvalidQueryStructure, Message: message}
	}
	if request.Anchor.ID == "" || request.Anchor.Timestamp.IsZero() {
		return nil, invalid("anchor id and timestamp are required")
	}
	if request.Direction != "" && request.Direction != logs_core.ContextBefore &&
		request.Direction != logs_core.ContextAfter {
		return nil, invalid(`direction must be "before" or "after"`)
	}
	if request.Cursor != "" {
		if _, err := logs_core.DecodeCursor(request.Cursor); err != nil {
			return nil, invalid("invalid cursor")
		}
	}

	queryID := uuid.New().String()
	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		return nil, errors.New("insufficient permissions to query project logs")
	}

	if err := s.queryValidator.ValidateQuery(request.Query); err != nil {
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}

	request.DefaultExtraFields = logs_core.ContextExtraFields(config.GetEnv().LogContextDefaultFields)
	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	return logs_core.GetLogCoreRepository().ExecuteContext(ctx, projectID, request)
}

// maxSQLQueryLength caps the raw SQL text: a huge string burns server time
// before segments.ValidateSingleStatementSelect even scans it.
const maxSQLQueryLength = 10_000

// ExecuteSQLQuery is SQL mode (see logs_querier.ExecuteSQLQuery for the
// security model); it shares ExecuteQuery's limiter and time-range rules.
func (s *LogQueryService) ExecuteSQLQuery(
	projectID uuid.UUID,
	request *logs_core.ExecuteSQLQueryRequestDTO,
	user *users_models.User,
) (*logs_core.SQLQueryResponseDTO, error) {
	queryID := uuid.New().String()

	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	defer s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID)

	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to query project logs")
	}

	if strings.TrimSpace(request.SQL) == "" {
		return nil, &ValidationError{
			Code:    logs_core.ErrorInvalidQueryStructure,
			Message: "sql is required",
		}
	}
	if len(request.SQL) > maxSQLQueryLength {
		return nil, &ValidationError{
			Code:    logs_core.ErrorQueryTooComplex,
			Message: fmt.Sprintf("query exceeds the %d character limit", maxSQLQueryLength),
		}
	}

	if err := s.validateTimeRange(request.TimeRange); err != nil {
		return nil, err
	}
	// The whole range is materialized before the query runs, so an open
	// start would copy the project's whole history.
	if request.TimeRange.From == nil {
		return nil, &ValidationError{
			Code:    logs_core.ErrorInvalidQueryStructure,
			Message: "timeRange.from is required in SQL mode",
		}
	}

	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	response, err := logs_core.GetLogCoreRepository().ExecuteSQLQuery(projectID, request)
	return response, err
}

func (s *LogQueryService) GetQueryableFields(
	projectID uuid.UUID,
	request *logs_core.GetQueryableFieldsRequestDTO,
	user *users_models.User,
) (*logs_core.GetQueryableFieldsResponseDTO, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to view project fields")
	}

	discoveredFieldNames, err := logs_core.GetLogCoreRepository().DiscoverFields(projectID)
	if err != nil {
		s.logger.Warn("Failed to discover fields from logs storage, using predefined fields only",
			slog.String("error", err.Error()),
			slog.String("projectId", projectID.String()))
		discoveredFieldNames = []string{} // Continue with predefined fields only
	}

	allFields := s.combineFields(discoveredFieldNames)

	filteredFields := s.filterFields(allFields, request.Query)

	// Enough for every attribute a project realistically has; the sidebar
	// lists them all.
	const maxFields = 500
	if len(filteredFields) > maxFields {
		filteredFields = filteredFields[:maxFields]
	}

	return &logs_core.GetQueryableFieldsResponseDTO{
		Fields: filteredFields,
	}, nil
}

func (s *LogQueryService) GetProjectStats(
	projectID uuid.UUID,
	user *users_models.User,
) (*logs_core.LogsStatsDTO, error) {
	s.logger.Info("Starting project stats request",
		"projectId", projectID.String(),
		"userId", user.ID.String())

	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		s.logger.Error("Failed to verify project access for stats",
			"projectId", projectID.String(),
			"userId", user.ID.String(),
			"error", err.Error())
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		s.logger.Warn("User lacks permission to view project stats",
			"projectId", projectID.String(),
			"userId", user.ID.String())
		return nil, errors.New("insufficient permissions to view project stats")
	}

	s.logger.Info("User authorized to view project stats, calling repository",
		"projectId", projectID.String(),
		"userId", user.ID.String())

	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	if err != nil {
		s.logger.Error("Repository failed to get project stats",
			"projectId", projectID.String(),
			"userId", user.ID.String(),
			"error", err.Error())
		return nil, fmt.Errorf("failed to get project stats: %w", err)
	}

	s.logger.Info("Successfully retrieved project stats from repository",
		"projectId", projectID.String(),
		"userId", user.ID.String(),
		"totalLogs", stats.TotalLogs,
		"totalSizeMB", stats.TotalSizeMB,
		"oldestLogTime", stats.OldestLogTime.Format("2006-01-02T15:04:05.000Z07:00"),
		"newestLogTime", stats.NewestLogTime.Format("2006-01-02T15:04:05.000Z07:00"),
		"oldestLogTimeIsZero", stats.OldestLogTime.IsZero(),
		"newestLogTimeIsZero", stats.NewestLogTime.IsZero())

	return stats, nil
}

func (s *LogQueryService) GetSystemStats(
	user *users_models.User,
) (*logs_core.LogsStatsDTO, error) {
	if !user.CanManageUsers() {
		return nil, errors.New("insufficient permissions to view system stats")
	}

	s.logger.Info("Admin user requesting system-wide stats",
		"userId", user.ID.String(),
		"userEmail", user.Email)

	stats, err := logs_core.GetLogCoreRepository().GetSystemLogStats()
	if err != nil {
		s.logger.Error("Repository failed to get system stats",
			"userId", user.ID.String(),
			"error", err.Error())
		return nil, fmt.Errorf("failed to get system stats: %w", err)
	}

	s.logger.Info("Successfully retrieved system stats",
		"userId", user.ID.String(),
		"totalLogs", stats.TotalLogs,
		"totalSizeMB", stats.TotalSizeMB)

	return stats, nil
}

func (s *LogQueryService) GetSystemDashboard(
	user *users_models.User,
) (*logs_core.SystemDashboardDTO, error) {
	if !user.CanManageUsers() {
		return nil, errors.New("insufficient permissions to view system dashboard")
	}

	stats, err := logs_core.GetLogCoreRepository().GetSystemLogStats()
	if err != nil {
		return nil, fmt.Errorf("failed to get system log stats: %w", err)
	}

	ingestionRate, err := system_metrics.GetIngestionRateLastNMinutes(ingestionRateWindowMinutes)
	if err != nil {
		// Ingestion rate is best-effort (Valkey, not the log data): report zero
		// rather than fail the dashboard.
		s.logger.Warn("Failed to read ingestion rate, reporting zero", "error", err.Error())
		ingestionRate = &system_metrics.IngestionRate{WindowMinutes: ingestionRateWindowMinutes}
	}

	hostMetrics, err := system_metrics.GetHostMetrics()
	if err != nil {
		s.logger.Warn("Failed to sample host CPU/memory metrics", "error", err.Error())
		hostMetrics = &system_metrics.HostMetrics{}
	}

	diskUsage, err := disk.GetDiskService().GetDiskUsage()
	if err != nil {
		s.logger.Warn("Failed to sample host disk usage", "error", err.Error())
		diskUsage = &disk.DiskUsage{}
	}

	return &logs_core.SystemDashboardDTO{
		TotalLogs:     stats.TotalLogs,
		TotalSizeMB:   stats.TotalSizeMB,
		RawSizeMB:     stats.RawSizeMB,
		OldestLogTime: stats.OldestLogTime,
		NewestLogTime: stats.NewestLogTime,

		IngestionWindowMinutes: ingestionRate.WindowMinutes,
		IngestedCount:          ingestionRate.TotalCount,
		IngestedRawBytes:       ingestionRate.TotalRawBytes,
		AvgIngestionRatePerMin: ingestionRate.AvgCountPerMin,

		CPUPercent:       hostMetrics.CPUPercent,
		MemoryUsedBytes:  hostMetrics.MemoryUsedBytes,
		MemoryTotalBytes: hostMetrics.MemoryTotalBytes,
		DiskUsedBytes:    diskUsage.UsedSpaceBytes,
		DiskTotalBytes:   diskUsage.TotalSpaceBytes,
	}, nil
}

func (s *LogQueryService) combineFields(discoveredFieldNames []string) []logs_core.QueryableField {
	fieldMap := make(map[string]logs_core.QueryableField)
	for _, field := range logs_core.PredefinedQueryableFields {
		fieldMap[field.Name] = field
	}

	for _, fieldName := range discoveredFieldNames {
		if _, exists := fieldMap[fieldName]; exists {
			continue
		}

		if s.isInternalField(fieldName) {
			continue
		}

		customField := logs_core.QueryableField{
			Name:     fieldName,
			Type:     logs_core.QueryableFieldTypeString, // Default to string for custom fields
			IsCustom: true,
			Operations: []logs_core.ConditionOperator{
				logs_core.ConditionOperatorEquals, logs_core.ConditionOperatorNotEquals,
				logs_core.ConditionOperatorContains, logs_core.ConditionOperatorNotContains,
				logs_core.ConditionOperatorExists, logs_core.ConditionOperatorNotExists,
			},
		}

		fieldMap[customField.Name] = customField
	}

	// Predefined fields first in declared order, then custom ones A-Z, so the
	// maxFields cut is deterministic.
	fields := make([]logs_core.QueryableField, 0, len(fieldMap))
	for _, field := range logs_core.PredefinedQueryableFields {
		fields = append(fields, fieldMap[field.Name])
		delete(fieldMap, field.Name)
	}
	custom := make([]logs_core.QueryableField, 0, len(fieldMap))
	for _, field := range fieldMap {
		custom = append(custom, field)
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })

	return append(fields, custom...)
}

func (s *LogQueryService) filterFields(fields []logs_core.QueryableField, query string) []logs_core.QueryableField {
	if query == "" {
		return fields // Return all fields if no query specified
	}

	query = strings.ToLower(query)
	filteredFields := make([]logs_core.QueryableField, 0)

	for _, field := range fields {
		if strings.Contains(strings.ToLower(field.Name), query) {
			filteredFields = append(filteredFields, field)
		}
	}

	return filteredFields
}

func (s *LogQueryService) isInternalField(fieldName string) bool {
	internalFields := map[string]bool{
		"_msg":    true,
		"_time":   true,
		"_stream": true,
		"project": true, // Our internal project field
	}
	return internalFields[fieldName]
}

func (s *LogQueryService) GetUserActiveQueryCount(userID uuid.UUID) (int, error) {
	return s.concurrentQueryLimiter.GetActiveQueryCount(userID)
}

func (s *LogQueryService) CleanupPendingQueries() error {
	if err := s.concurrentQueryLimiter.CleanupAllQuerySlots(); err != nil {
		return fmt.Errorf("failed to cleanup query slots on startup: %w", err)
	}

	return nil
}

func (s *LogQueryService) validateTimeRange(timeRange *logs_core.TimeRangeDTO) error {
	if timeRange == nil {
		return &ValidationError{
			Code:    logs_core.ErrorMissingTimeRangeTo,
			Message: "timeRange is required for pagination consistency",
		}
	}

	if timeRange.To == nil {
		return &ValidationError{
			Code:    logs_core.ErrorMissingTimeRangeTo,
			Message: "timeRange.to is required for pagination consistency to prevent issues when new logs are inserted",
		}
	}

	if timeRange.From != nil && timeRange.To != nil {
		if timeRange.From.After(*timeRange.To) {
			return &ValidationError{
				Code:    logs_core.ErrorInvalidQueryStructure,
				Message: "timeRange.from must be before timeRange.to",
			}
		}
	}

	return nil
}
