package dashboards_dto

import (
	"time"

	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

type CreateDashboardRequestDTO struct {
	Title       string                       `json:"title"       binding:"required,min=1,max=255"`
	Description string                       `json:"description"`
	Tabs        []dashboards_models.Tab      `json:"tabs"`
	Variables   []dashboards_models.Variable `json:"variables"`
}

type UpdateDashboardRequestDTO struct {
	Title       string                       `json:"title"       binding:"required,min=1,max=255"`
	Description string                       `json:"description"`
	Tabs        []dashboards_models.Tab      `json:"tabs"`
	Variables   []dashboards_models.Variable `json:"variables"`
}

type SetPinnedRequestDTO struct {
	Pinned bool `json:"pinned"`
}

type CreateFolderRequestDTO struct {
	Title string `json:"title" binding:"required,min=1,max=255"`
}

type RenameFolderRequestDTO struct {
	Title string `json:"title" binding:"required,min=1,max=255"`
}

type ListFoldersResponseDTO struct {
	Folders []*dashboards_models.Folder `json:"folders"`
}

// SetDashboardFolderRequestDTO uses a pointer: an explicit null un-files the
// dashboard.
type SetDashboardFolderRequestDTO struct {
	FolderID *uuid.UUID `json:"folderId"`
}

type ListDashboardsResponseDTO struct {
	Dashboards []*dashboards_models.Dashboard `json:"dashboards"`
}

// PanelQueryRequestDTO carries the full panel definition, so unsaved panels
// can be previewed.
type PanelQueryRequestDTO struct {
	Panel     dashboards_models.Panel `json:"panel"`
	TimeRange *logs_core.TimeRangeDTO `json:"timeRange,omitempty"`
}

// PanelBucketDTO is one shape for every signal's panel data (logs count as
// int64, spans and metrics as float64), so the frontend renders them alike.
type PanelBucketDTO struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	GroupKey  string     `json:"groupKey,omitempty"`
	Value     float64    `json:"value"`
}

type PanelQueryResponseDTO struct {
	Buckets []PanelBucketDTO `json:"buckets"`
}

// Streamed panel query frames (/query/stream).
const (
	PanelFrameBuckets   = "buckets"
	PanelFrameHeartbeat = "heartbeat"
	PanelFrameDone      = "done"
	PanelFrameError     = "error"
)

// PanelFrameDTO is one line of a streamed panel query: "buckets" (to add to
// the earlier frames' buckets), "heartbeat", "done", or "error".
type PanelFrameDTO struct {
	Type    string           `json:"type"`
	Buckets []PanelBucketDTO `json:"buckets,omitempty"`
	Status  int              `json:"status,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// ImportDashboardRequestDTO is a dashboard JSON export; Format picks the parser.
type ImportDashboardRequestDTO struct {
	Format string `json:"format" binding:"required,oneof=grafana openobserve"`
	JSON   string `json:"json"   binding:"required"`
}

type ImportDashboardResponseDTO struct {
	Dashboard *dashboards_models.Dashboard `json:"dashboard"`
	// Warnings are import caveats for the user, such as panels left without
	// a query.
	Warnings []string `json:"warnings,omitempty"`
}
