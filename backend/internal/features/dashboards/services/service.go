// Package dashboards_services implements project-scoped dashboards. Any
// member with project write access may change them; viewers may only read.
package dashboards_services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obzly/internal/authz"
	"regexp"
	"sort"
	"strings"
	"time"

	"obzly/internal/config"
	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	dashboards_repositories "obzly/internal/features/dashboards/repositories"
	logs_core "obzly/internal/features/logs/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	projects_services "obzly/internal/features/projects/services"
	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/querylimit"

	"github.com/google/uuid"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

// Limits per tab and per dashboard: capping panels per tab alone left the
// document unbounded (any number of tabs).
const (
	maxPanelsPerTab       = 100
	maxTabsPerDashboard   = 50
	maxPanelsPerDashboard = 500
	// Variables and free text are bounded too, so one dashboard document
	// stays small however it is built.
	maxVariablesPerDashboard   = 50
	maxStaticValuesPerVariable = 1000
	maxTextBytes               = 64 << 10
	maxImportJSONBytes         = 4 << 20
)

type DashboardService struct {
	dashboardRepository *dashboards_repositories.DashboardRepository
	folderRepository    *dashboards_repositories.FolderRepository
	projectService      *projects_services.ProjectService
	panelQueryLimiter   *querylimit.Limiter
}

func (s *DashboardService) ListDashboards(
	projectID uuid.UUID,
	user *users_models.User,
) (*dashboards_dto.ListDashboardsResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	dashboards, err := s.dashboardRepository.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list dashboards: %w", err)
	}

	return &dashboards_dto.ListDashboardsResponseDTO{Dashboards: dashboards}, nil
}

func (s *DashboardService) GetDashboard(
	projectID, dashboardID uuid.UUID,
	user *users_models.User,
) (*dashboards_models.Dashboard, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	return s.getOwnedDashboard(projectID, dashboardID)
}

func (s *DashboardService) CreateDashboard(
	projectID uuid.UUID,
	request *dashboards_dto.CreateDashboardRequestDTO,
	user *users_models.User,
) (*dashboards_models.Dashboard, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	if len(request.Description) > maxTextBytes {
		return nil, fmt.Errorf("the description can't be longer than %d bytes", maxTextBytes)
	}
	tabs, err := normalizeTabs(request.Tabs)
	if err != nil {
		return nil, err
	}

	variables, err := normalizeVariables(request.Variables)
	if err != nil {
		return nil, err
	}

	dashboard := &dashboards_models.Dashboard{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       request.Title,
		Description: request.Description,
		Tabs:        tabs,
		Variables:   variables,
		CreatedBy:   &user.ID,
	}

	if err := s.dashboardRepository.Create(dashboard); err != nil {
		return nil, fmt.Errorf("failed to create dashboard: %w", err)
	}

	return dashboard, nil
}

func (s *DashboardService) UpdateDashboard(
	projectID, dashboardID uuid.UUID,
	request *dashboards_dto.UpdateDashboardRequestDTO,
	user *users_models.User,
) (*dashboards_models.Dashboard, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	dashboard, err := s.getOwnedDashboard(projectID, dashboardID)
	if err != nil {
		return nil, err
	}

	if len(request.Description) > maxTextBytes {
		return nil, fmt.Errorf("the description can't be longer than %d bytes", maxTextBytes)
	}
	tabs, err := normalizeTabs(request.Tabs)
	if err != nil {
		return nil, err
	}

	variables, err := normalizeVariables(request.Variables)
	if err != nil {
		return nil, err
	}

	dashboard.Title = request.Title
	dashboard.Description = request.Description
	dashboard.Tabs = tabs
	dashboard.Variables = variables

	if err := s.dashboardRepository.Update(dashboard); err != nil {
		return nil, fmt.Errorf("failed to update dashboard: %w", err)
	}

	return dashboard, nil
}

func (s *DashboardService) SetPinned(
	projectID, dashboardID uuid.UUID,
	pinned bool,
	user *users_models.User,
) (*dashboards_models.Dashboard, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	dashboard, err := s.getOwnedDashboard(projectID, dashboardID)
	if err != nil {
		return nil, err
	}

	if err := s.dashboardRepository.SetPinned(dashboardID, pinned); err != nil {
		return nil, fmt.Errorf("failed to update dashboard pin state: %w", err)
	}

	dashboard.IsPinned = pinned
	return dashboard, nil
}

func (s *DashboardService) DeleteDashboard(projectID, dashboardID uuid.UUID, user *users_models.User) error {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return err
	}

	if _, err := s.getOwnedDashboard(projectID, dashboardID); err != nil {
		return err
	}

	if err := s.dashboardRepository.Delete(dashboardID); err != nil {
		return fmt.Errorf("failed to delete dashboard: %w", err)
	}

	return nil
}

// ExecutePanelQuery runs one panel's query. It takes the panel definition,
// not a saved ID, so unsaved panels can be previewed.
func (s *DashboardService) ExecutePanelQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
	user *users_models.User,
) (*dashboards_dto.PanelQueryResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	if err := validatePanelTimeRange(request); err != nil {
		return nil, err
	}
	release, err := s.panelQueryLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	switch request.Panel.EffectiveSource() {
	case dashboards_models.PanelSourceSpans:
		return s.executeSpanPanelQuery(projectID, request)
	case dashboards_models.PanelSourceMetrics:
		return s.executeMetricPanelQuery(projectID, request)
	default:
		return s.executeLogPanelQuery(ctx, projectID, request)
	}
}

// StreamPanelQuery is ExecutePanelQuery emitting a time-bucketed logs panel
// partition group by partition group (anything else in one call). start runs
// after the checks, so a refused query is still a plain error.
func (s *DashboardService) StreamPanelQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
	user *users_models.User,
	start func(),
	emit func([]dashboards_dto.PanelBucketDTO) error,
) error {
	if request.Panel.EffectiveSource() != dashboards_models.PanelSourceLogs || request.Panel.BucketMs <= 0 {
		response, err := s.ExecutePanelQuery(ctx, projectID, request, user)
		if err != nil {
			return err
		}
		start()
		return emit(response.Buckets)
	}
	if err := s.requireAccess(projectID, user); err != nil {
		return err
	}
	if err := validatePanelTimeRange(request); err != nil {
		return err
	}
	release, err := s.panelQueryLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return err
	}
	defer release()
	start()
	_, err = logs_core.GetLogCoreRepository().StreamAggregation(ctx, projectID, logAggregationRequest(request),
		func(buckets []logs_core.AggregationBucketDTO) error { return emit(panelBuckets(buckets)) })
	if err != nil {
		return fmt.Errorf("failed to execute panel query: %w", err)
	}
	return nil
}

// validatePanelTimeRange requires a logs or spans panel's range to be
// closed, as a log search's is: an open one reads the project's whole
// history. A metrics panel defaults to the last hour.
func validatePanelTimeRange(request *dashboards_dto.PanelQueryRequestDTO) error {
	if request.Panel.EffectiveSource() == dashboards_models.PanelSourceMetrics {
		return nil
	}
	timeRange := request.TimeRange
	if timeRange == nil || timeRange.From == nil || timeRange.To == nil {
		return errors.New("timeRange.from and timeRange.to are required")
	}
	if timeRange.From.After(*timeRange.To) {
		return errors.New("timeRange.from must be before timeRange.to")
	}
	return nil
}

func logAggregationRequest(request *dashboards_dto.PanelQueryRequestDTO) *logs_core.AggregationRequestDTO {
	return &logs_core.AggregationRequestDTO{
		Query:      request.Panel.Query,
		TimeRange:  request.TimeRange,
		GroupField: request.Panel.GroupField,
		BucketMs:   request.Panel.BucketMs,
		Limit:      request.Panel.Limit,
		ScanBudget: config.GetEnv().QueryMaxScanBytes,
	}
}

func panelBuckets(buckets []logs_core.AggregationBucketDTO) []dashboards_dto.PanelBucketDTO {
	panel := make([]dashboards_dto.PanelBucketDTO, len(buckets))
	for i, bucket := range buckets {
		panel[i] = dashboards_dto.PanelBucketDTO{
			Timestamp: bucket.Timestamp,
			GroupKey:  bucket.GroupKey,
			Value:     float64(bucket.Count),
		}
	}
	return panel
}

func (s *DashboardService) executeLogPanelQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
) (*dashboards_dto.PanelQueryResponseDTO, error) {
	response, err := logs_core.GetLogCoreRepository().ExecuteAggregation(ctx, projectID,
		logAggregationRequest(request))
	if err != nil {
		return nil, fmt.Errorf("failed to execute panel query: %w", err)
	}
	return &dashboards_dto.PanelQueryResponseDTO{Buckets: panelBuckets(response.Buckets)}, nil
}

func (s *DashboardService) executeSpanPanelQuery(
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
) (*dashboards_dto.PanelQueryResponseDTO, error) {
	metric := spans_core.AggregationMetric(request.Panel.Metric)
	if metric == "" {
		metric = spans_core.AggregationMetricCount
	}
	if !metric.IsValid() {
		return nil, fmt.Errorf("invalid panel metric %q", request.Panel.Metric)
	}

	groupField := spans_core.GroupField(request.Panel.GroupField)
	if groupField != "" && !groupField.IsValid() {
		return nil, fmt.Errorf("invalid panel group field %q", request.Panel.GroupField)
	}

	var timeRange *spans_core.TimeRangeDTO
	if request.TimeRange != nil {
		timeRange = &spans_core.TimeRangeDTO{From: request.TimeRange.From, To: request.TimeRange.To}
	}

	aggregationRequest := &spans_core.AggregationRequestDTO{
		TimeRange:     timeRange,
		ServiceName:   request.Panel.ServiceName,
		OperationName: request.Panel.OperationName,
		RootSpansOnly: request.Panel.RootSpansOnly,
		GroupField:    groupField,
		BucketMs:      request.Panel.BucketMs,
		Metric:        metric,
		Limit:         request.Panel.Limit,
		ScanBudget:    config.GetEnv().QueryMaxScanBytes,
	}

	response, err := spans_core.GetSpanCoreRepository().ExecuteAggregation(projectID, aggregationRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to execute panel query: %w", err)
	}

	buckets := make([]dashboards_dto.PanelBucketDTO, len(response.Buckets))
	for i, bucket := range response.Buckets {
		buckets[i] = dashboards_dto.PanelBucketDTO{
			Timestamp: bucket.Timestamp,
			GroupKey:  bucket.GroupKey,
			Value:     bucket.Value,
		}
	}
	return &dashboards_dto.PanelQueryResponseDTO{Buckets: buckets}, nil
}

// executeMetricPanelQuery runs the panel's PromQL as a range query; each
// series becomes one GroupKey with a bucket per point.
func (s *DashboardService) executeMetricPanelQuery(
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
) (*dashboards_dto.PanelQueryResponseDTO, error) {
	query := metrics_promql.NormalizePromQL(strings.TrimSpace(request.Panel.PromQuery))
	if query == "" {
		return &dashboards_dto.PanelQueryResponseDTO{Buckets: []dashboards_dto.PanelBucketDTO{}}, nil
	}

	start, end := resolveMetricPanelTimeRange(request.TimeRange)
	step := metricPanelStep(request.Panel.BucketMs, start, end)

	if err := metrics_promql.ValidateRangeQueryResolution(start, end, step); err != nil {
		return nil, err
	}

	rangeQuery, err := metrics_promql.NewRangeQuery(context.Background(), projectID, query, start, end, step)
	if err != nil {
		return nil, fmt.Errorf("invalid PromQL query: %w", err)
	}
	defer rangeQuery.Close()

	result := rangeQuery.Exec(context.Background())
	if result.Err != nil {
		return nil, fmt.Errorf("failed to execute panel query: %w", result.Err)
	}

	matrix, ok := result.Value.(promql.Matrix)
	if !ok {
		return nil, errors.New("panel query must produce a range vector result")
	}

	buckets := make([]dashboards_dto.PanelBucketDTO, 0)
	for _, series := range matrix {
		groupKey := metricSeriesLabel(series.Metric)
		for _, point := range series.Floats {
			ts := time.UnixMilli(point.T).UTC()
			buckets = append(buckets, dashboards_dto.PanelBucketDTO{Timestamp: &ts, GroupKey: groupKey, Value: point.F})
		}
	}

	return &dashboards_dto.PanelQueryResponseDTO{Buckets: buckets}, nil
}

// resolveMetricPanelTimeRange defaults to the last hour: a PromQL range
// query needs concrete bounds.
func resolveMetricPanelTimeRange(timeRange *logs_core.TimeRangeDTO) (time.Time, time.Time) {
	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour)
	end := now

	if timeRange != nil {
		if timeRange.From != nil {
			start = *timeRange.From
		}
		if timeRange.To != nil {
			end = *timeRange.To
		}
	}

	return start, end
}

// metricPanelStep is bucketMs, or for 0 about 120 points across the range.
func metricPanelStep(bucketMs int64, start, end time.Time) time.Duration {
	// Floor a supplied bucketMs too: a 1 ms step over days is millions of
	// evaluation points per render.
	if bucketMs > 0 {
		step := time.Duration(bucketMs) * time.Millisecond
		if step < time.Second {
			step = time.Second
		}
		return step
	}

	step := end.Sub(start) / 120
	if step < time.Second {
		step = time.Second
	}
	return step
}

// metricSeriesLabel is a compact legend ("service=api, code=200") without
// __name__, which the query already names.
func metricSeriesLabel(lbls labels.Labels) string {
	m := lbls.Map()
	delete(m, "__name__")
	if len(m) == 0 {
		return lbls.Get("__name__")
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ", ")
}

// ImportDashboard saves a Grafana or OpenObserve dashboard export as a new
// dashboard. Layout, titles and types translate; queries do not (see
// ARCHITECTURE.md "Dashboards").
func (s *DashboardService) ImportDashboard(
	projectID uuid.UUID,
	request *dashboards_dto.ImportDashboardRequestDTO,
	user *users_models.User,
) (*dashboards_dto.ImportDashboardResponseDTO, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	if len(request.JSON) > maxImportJSONBytes {
		return nil, fmt.Errorf("the dashboard JSON can't be larger than %d MiB", maxImportJSONBytes>>20)
	}

	var title string
	var panels dashboards_models.Panels
	var warnings []string
	var err error

	switch request.Format {
	case "grafana":
		title, panels, warnings, err = importGrafanaDashboard(request.JSON)
	case "openobserve":
		title, panels, warnings, err = importOpenObserveDashboard(request.JSON)
	default:
		return nil, fmt.Errorf("unsupported import format %q", request.Format)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to parse dashboard JSON: %w", err)
	}

	normalizedPanels, err := normalizePanels(panels)
	if err != nil {
		return nil, err
	}
	for i := range normalizedPanels {
		normalizePanelNames(&normalizedPanels[i])
	}

	dashboard := &dashboards_models.Dashboard{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: fmt.Sprintf("Imported from %s.", request.Format),
		// Importers flatten source tabs into one panel list.
		Tabs:      dashboards_models.Tabs{{ID: uuid.New().String(), Title: "Default", Panels: normalizedPanels}},
		CreatedBy: &user.ID,
	}

	if err := s.dashboardRepository.Create(dashboard); err != nil {
		return nil, fmt.Errorf("failed to save imported dashboard: %w", err)
	}

	return &dashboards_dto.ImportDashboardResponseDTO{Dashboard: dashboard, Warnings: warnings}, nil
}

// OnBeforeProjectDeletion implements projects_interfaces.ProjectDeletionListener.
func (s *DashboardService) OnBeforeProjectDeletion(projectID uuid.UUID) error {
	if err := s.dashboardRepository.DeleteAllForProject(projectID); err != nil {
		return fmt.Errorf("failed to delete project dashboards: %w", err)
	}
	if err := s.folderRepository.DeleteAllForProject(projectID); err != nil {
		return fmt.Errorf("failed to delete project dashboard folders: %w", err)
	}
	return nil
}

func (s *DashboardService) ListFolders(
	projectID uuid.UUID,
	user *users_models.User,
) (*dashboards_dto.ListFoldersResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	folders, err := s.folderRepository.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list dashboard folders: %w", err)
	}

	return &dashboards_dto.ListFoldersResponseDTO{Folders: folders}, nil
}

func (s *DashboardService) CreateFolder(
	projectID uuid.UUID,
	request *dashboards_dto.CreateFolderRequestDTO,
	user *users_models.User,
) (*dashboards_models.Folder, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	folder := &dashboards_models.Folder{ID: uuid.New(), ProjectID: projectID, Title: request.Title}
	if err := s.folderRepository.Create(folder); err != nil {
		return nil, fmt.Errorf("failed to create dashboard folder: %w", err)
	}
	return folder, nil
}

func (s *DashboardService) RenameFolder(
	projectID, folderID uuid.UUID,
	request *dashboards_dto.RenameFolderRequestDTO,
	user *users_models.User,
) (*dashboards_models.Folder, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	folder, err := s.getOwnedFolder(projectID, folderID)
	if err != nil {
		return nil, err
	}

	if err := s.folderRepository.Rename(folderID, request.Title); err != nil {
		return nil, fmt.Errorf("failed to rename dashboard folder: %w", err)
	}
	folder.Title = request.Title
	return folder, nil
}

func (s *DashboardService) DeleteFolder(projectID, folderID uuid.UUID, user *users_models.User) error {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return err
	}
	if _, err := s.getOwnedFolder(projectID, folderID); err != nil {
		return err
	}

	// folder_id is ON DELETE SET NULL: the dashboards are un-filed, not deleted.
	if err := s.folderRepository.Delete(folderID); err != nil {
		return fmt.Errorf("failed to delete dashboard folder: %w", err)
	}
	return nil
}

// SetDashboardFolder moves a dashboard into folderID, or un-files it when
// folderID is nil.
func (s *DashboardService) SetDashboardFolder(
	projectID, dashboardID uuid.UUID,
	folderID *uuid.UUID,
	user *users_models.User,
) (*dashboards_models.Dashboard, error) {
	if err := s.requireWriteAccess(projectID, user); err != nil {
		return nil, err
	}

	dashboard, err := s.getOwnedDashboard(projectID, dashboardID)
	if err != nil {
		return nil, err
	}
	if folderID != nil {
		if _, err := s.getOwnedFolder(projectID, *folderID); err != nil {
			return nil, err
		}
	}

	if err := s.dashboardRepository.SetFolder(dashboardID, folderID); err != nil {
		return nil, fmt.Errorf("failed to move dashboard: %w", err)
	}
	dashboard.FolderID = folderID
	return dashboard, nil
}

func (s *DashboardService) getOwnedFolder(projectID, folderID uuid.UUID) (*dashboards_models.Folder, error) {
	folder, err := s.folderRepository.GetByID(folderID)
	if err != nil {
		return nil, errors.New("dashboard folder not found")
	}
	if folder.ProjectID != projectID {
		return nil, errors.New("dashboard folder not found")
	}
	return folder, nil
}

func (s *DashboardService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to check project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to access project dashboards")
	}
	return nil
}

// requireWriteAccess gates every dashboard mutation; a PROJECT_VIEWER passes
// requireAccess but not this.
func (s *DashboardService) requireWriteAccess(projectID uuid.UUID, user *users_models.User) error {
	decision, err := authz.Authorize(context.Background(), user, authz.ProjectWrite, authz.Project(projectID))
	if err != nil {
		return fmt.Errorf("failed to check project access: %w", err)
	}
	if !decision.Allowed {
		return errors.New("insufficient permissions to modify project dashboards")
	}
	return nil
}

func (s *DashboardService) getOwnedDashboard(projectID, dashboardID uuid.UUID) (*dashboards_models.Dashboard, error) {
	dashboard, err := s.dashboardRepository.GetByID(dashboardID)
	if err != nil {
		return nil, errors.New("dashboard not found")
	}
	if dashboard.ProjectID != projectID {
		return nil, errors.New("dashboard not found")
	}
	return dashboard, nil
}

// normalizePanels validates panels and assigns an ID to any without one.
func normalizePanels(panels []dashboards_models.Panel) (dashboards_models.Panels, error) {
	if len(panels) > maxPanelsPerTab {
		return nil, fmt.Errorf("a tab cannot have more than %d panels", maxPanelsPerTab)
	}

	normalized := make(dashboards_models.Panels, len(panels))
	for i, panel := range panels {
		if !panel.Type.IsValid() {
			return nil, fmt.Errorf("invalid panel type %q", panel.Type)
		}
		if !panel.Source.IsValid() {
			return nil, fmt.Errorf("invalid panel source %q", panel.Source)
		}
		if !panel.Unit.IsValid() {
			return nil, fmt.Errorf("invalid panel unit %q", panel.Unit)
		}
		if len(panel.PromQuery) > maxTextBytes {
			return nil, fmt.Errorf("a panel query can't be longer than %d bytes", maxTextBytes)
		}
		if panel.Query != nil {
			if encoded, err := json.Marshal(panel.Query); err != nil || len(encoded) > maxTextBytes {
				return nil, fmt.Errorf("a panel query can't be longer than %d bytes", maxTextBytes)
			}
		}

		if panel.EffectiveSource() == dashboards_models.PanelSourceSpans {
			if panel.GroupField != "" && !spans_core.GroupField(panel.GroupField).IsValid() {
				return nil, fmt.Errorf("invalid panel group field %q for a spans panel", panel.GroupField)
			}
			if panel.Metric != "" && !spans_core.AggregationMetric(panel.Metric).IsValid() {
				return nil, fmt.Errorf("invalid panel metric %q", panel.Metric)
			}
		}

		if panel.ID == "" {
			panel.ID = uuid.New().String()
		}
		normalized[i] = panel
	}

	return normalized, nil
}

// normalizeTabs validates tabs and fills missing IDs and titles. It enforces
// that a dashboard always has at least one tab (empty input: one "Default").
func normalizeTabs(tabs []dashboards_models.Tab) (dashboards_models.Tabs, error) {
	if len(tabs) == 0 {
		return dashboards_models.Tabs{
			{ID: uuid.New().String(), Title: "Default", Panels: dashboards_models.Panels{}},
		}, nil
	}

	if len(tabs) > maxTabsPerDashboard {
		return nil, fmt.Errorf("a dashboard cannot have more than %d tabs", maxTabsPerDashboard)
	}

	normalized := make(dashboards_models.Tabs, len(tabs))
	totalPanels := 0
	for i, tab := range tabs {
		panels, err := normalizePanels(tab.Panels)
		if err != nil {
			return nil, fmt.Errorf("tab %q: %w", tab.Title, err)
		}
		totalPanels += len(panels)

		if tab.ID == "" {
			tab.ID = uuid.New().String()
		}
		if tab.Title == "" {
			tab.Title = "Untitled tab"
		}
		tab.Panels = panels
		normalized[i] = tab
	}

	if totalPanels > maxPanelsPerDashboard {
		return nil, fmt.Errorf("a dashboard cannot have more than %d panels in total", maxPanelsPerDashboard)
	}

	return normalized, nil
}

// variableKeyPattern must stay in sync with the frontend's
// substituteVariables regex (dashboardVariables.ts).
var variableKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// normalizeVariables validates variables and rejects duplicate keys, which
// would make ${key} substitution ambiguous.
func normalizeVariables(variables []dashboards_models.Variable) (dashboards_models.Variables, error) {
	if len(variables) > maxVariablesPerDashboard {
		return nil, fmt.Errorf("a dashboard cannot have more than %d variables", maxVariablesPerDashboard)
	}
	seen := make(map[string]bool, len(variables))
	normalized := make(dashboards_models.Variables, len(variables))
	for i, variable := range variables {
		if !variableKeyPattern.MatchString(variable.Key) {
			return nil, fmt.Errorf(
				"invalid variable key %q - must start with a letter or underscore and contain only letters, digits, underscores",
				variable.Key,
			)
		}
		if seen[variable.Key] {
			return nil, fmt.Errorf("duplicate variable key %q", variable.Key)
		}
		seen[variable.Key] = true

		if !variable.Source.IsValid() {
			return nil, fmt.Errorf("invalid variable source %q", variable.Source)
		}
		if variable.Source == dashboards_models.VariableSourceMetricLabel &&
			strings.TrimSpace(variable.MetricLabelName) == "" {
			return nil, fmt.Errorf("variable %q: metricLabelName is required for source metricLabel", variable.Key)
		}
		if variable.Source == dashboards_models.VariableSourceStatic && len(variable.StaticValues) == 0 {
			return nil, fmt.Errorf("variable %q: staticValues is required for source static", variable.Key)
		}
		if len(variable.StaticValues) > maxStaticValuesPerVariable {
			return nil, fmt.Errorf("variable %q: at most %d static values", variable.Key, maxStaticValuesPerVariable)
		}
		size := len(variable.Label) + len(variable.MetricLabelName) + len(variable.DefaultValue)
		for _, value := range variable.StaticValues {
			size += len(value)
		}
		if size > maxTextBytes {
			return nil, fmt.Errorf("variable %q: its values can't be longer than %d bytes", variable.Key, maxTextBytes)
		}

		normalized[i] = variable
	}

	return normalized, nil
}
