package logs_core

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LogRepository is the logs stream's storage abstraction. The implementation
// lives in logs/parquetstore (importing it here would be a cycle) and is wired
// in at startup via SetRepository.
type LogRepository interface {
	StoreLogsBatch(entries map[uuid.UUID][]*LogItem) error
	ExecuteQueryForProject(projectID uuid.UUID, request *LogQueryRequestDTO) (*LogQueryResponseDTO, error)
	// StreamQueryForProject is ExecuteQueryForProject handing emit each
	// search window's rows as they are read; ctx ending stops the search.
	StreamQueryForProject(
		ctx context.Context, projectID uuid.UUID, request *LogQueryRequestDTO, emit SearchEmitter,
	) (*LogQueryResponseDTO, error)
	ExecuteSQLQuery(projectID uuid.UUID, request *ExecuteSQLQueryRequestDTO) (*SQLQueryResponseDTO, error)
	ExecuteScopedSQL(projectID uuid.UUID, request *ScopedSQLRequestDTO) ([]*SQLQueryResponseDTO, error)
	// ExecuteAggregation stops when ctx ends (the client left), freeing executors.
	ExecuteAggregation(
		ctx context.Context, projectID uuid.UUID, request *AggregationRequestDTO,
	) (*AggregationResponseDTO, error)
	ExecutePatterns(projectID uuid.UUID, request *PatternsRequestDTO) (*PatternsResponseDTO, error)
	ExecuteFieldValues(
		ctx context.Context, projectID uuid.UUID, request *FieldValuesRequestDTO,
	) (*FieldValuesResponseDTO, error)
	// StreamAggregation is ExecuteAggregation emitting counts as read, bucketed
	// ones by partition group, newest first; each call's buckets add to earlier ones.
	StreamAggregation(
		ctx context.Context, projectID uuid.UUID, request *AggregationRequestDTO, emit AggregationEmitter,
	) (*AggregationResponseDTO, error)
	ExecutePatternChanges(
		ctx context.Context, projectID uuid.UUID, request *PatternChangesRequestDTO,
	) (*PatternChangesResponseDTO, error)
	ExecuteDeploys(
		ctx context.Context, projectID uuid.UUID, request *DeploysRequestDTO,
	) (*DeploysResponseDTO, error)
	ExecuteContext(
		ctx context.Context, projectID uuid.UUID, request *LogContextRequestDTO,
	) (*LogContextResponseDTO, error)
	DiscoverFields(projectID uuid.UUID) ([]string, error)
	GetProjectLogStats(projectID uuid.UUID) (*LogsStatsDTO, error)
	GetSystemLogStats() (*LogsStatsDTO, error)
	DeleteLogsByProject(projectID uuid.UUID) error
	DeleteOldLogs(projectID uuid.UUID, olderThan time.Time) error
	// CompactSmallSegments merges a project's small segments into larger ones.
	// It runs before upload, so object storage only sees full-sized segments.
	CompactSmallSegments(projectID uuid.UUID) error
	// UploadPendingSegments pushes not-yet-uploaded segments to object storage (a
	// no-op without it). Always run after CompactSmallSegments.
	UploadPendingSegments() error
	// ReconcileSegments re-downloads an uploaded segment whose local copy is
	// missing (no-op without object storage). Safe at any point: it never deletes.
	ReconcileSegments() error
	ForceFlush() error
	TestConnection() error
}
