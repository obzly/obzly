package logs_core

import (
	"context"
	"errors"
	"time"

	projects_services "obzly/internal/features/projects/services"

	"github.com/google/uuid"
)

// activeRepository is set once at startup via SetRepository. Never read it in
// package-level var initialization (that runs before main and would capture
// nil); call GetLogCoreRepository lazily.
var activeRepository LogRepository

// SetRepository wires the concrete LogRepository implementation. Call once,
// as one of the first things main() does.
func SetRepository(repo LogRepository) {
	activeRepository = repo
}

// GetLogCoreRepository returns the active LogRepository. Panics before
// SetRepository: a startup-wiring bug.
func GetLogCoreRepository() LogRepository {
	if activeRepository == nil {
		panic("logs_core: LogRepository accessed before SetRepository was called")
	}
	return activeRepository
}

// activeAIRepository stores StreamAILogs; same startup rules as activeRepository.
var activeAIRepository LogRepository

func SetAIRepository(repo LogRepository) {
	activeAIRepository = repo
}

// GetAIRepository returns the repository for AI tool telemetry (StreamAILogs).
func GetAIRepository() LogRepository {
	if activeAIRepository == nil {
		panic("logs_core: AI LogRepository accessed before SetAIRepository was called")
	}
	return activeAIRepository
}

// GetAllRepositories is every wired logs-schema repository, for the
// background workers (compaction, upload, retention) that must cover each.
func GetAllRepositories() []LogRepository {
	repositories := []LogRepository{GetLogCoreRepository()}
	if activeAIRepository != nil {
		repositories = append(repositories, activeAIRepository)
	}
	return repositories
}

// GetUnavailableLogCoreRepository returns a repository whose every method
// fails, for error-propagation tests.
func GetUnavailableLogCoreRepository() LogRepository {
	return unavailableRepository{}
}

var logCoreService = &LogCoreService{}

func SetupDependencies() {
	projects_services.GetProjectService().AddProjectDeletionListener(logCoreService)
}

var errRepositoryUnavailable = errors.New("logs storage is unavailable")

type unavailableRepository struct{}

func (unavailableRepository) StoreLogsBatch(map[uuid.UUID][]*LogItem) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ExecuteQueryForProject(
	uuid.UUID,
	*LogQueryRequestDTO,
) (*LogQueryResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) StreamQueryForProject(
	context.Context,
	uuid.UUID,
	*LogQueryRequestDTO,
	SearchEmitter,
) (*LogQueryResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteSQLQuery(
	uuid.UUID,
	*ExecuteSQLQueryRequestDTO,
) (*SQLQueryResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteScopedSQL(
	uuid.UUID,
	*ScopedSQLRequestDTO,
) ([]*SQLQueryResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteAggregation(
	context.Context,
	uuid.UUID,
	*AggregationRequestDTO,
) (*AggregationResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecutePatterns(
	uuid.UUID,
	*PatternsRequestDTO,
) (*PatternsResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteFieldValues(
	context.Context,
	uuid.UUID,
	*FieldValuesRequestDTO,
) (*FieldValuesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) StreamAggregation(
	context.Context,
	uuid.UUID,
	*AggregationRequestDTO,
	AggregationEmitter,
) (*AggregationResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecutePatternChanges(
	context.Context,
	uuid.UUID,
	*PatternChangesRequestDTO,
) (*PatternChangesResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteDeploys(
	context.Context,
	uuid.UUID,
	*DeploysRequestDTO,
) (*DeploysResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) ExecuteContext(
	context.Context,
	uuid.UUID,
	*LogContextRequestDTO,
) (*LogContextResponseDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) DiscoverFields(uuid.UUID) ([]string, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetProjectLogStats(uuid.UUID) (*LogsStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) GetSystemLogStats() (*LogsStatsDTO, error) {
	return nil, errRepositoryUnavailable
}

func (unavailableRepository) DeleteLogsByProject(uuid.UUID) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) DeleteOldLogs(uuid.UUID, time.Time) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) CompactSmallSegments(uuid.UUID) error {
	return errRepositoryUnavailable
}

func (unavailableRepository) UploadPendingSegments() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ReconcileSegments() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) ForceFlush() error {
	return errRepositoryUnavailable
}

func (unavailableRepository) TestConnection() error {
	return errRepositoryUnavailable
}
