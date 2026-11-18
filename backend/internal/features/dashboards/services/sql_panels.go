package dashboards_services

import (
	"context"
	"fmt"

	"obzly/internal/config"
	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

// sqlPanelTypes are the kinds a SQL result's shape can fill: a time column
// and numbers (line), a label and a number (rankedList, donut), the rows as
// they are (table), or the first number of the first row (stat).
var sqlPanelTypes = map[dashboards_models.PanelType]bool{
	dashboards_models.PanelTypeLine:       true,
	dashboards_models.PanelTypeRankedList: true,
	dashboards_models.PanelTypeDonut:      true,
	dashboards_models.PanelTypeTable:      true,
	dashboards_models.PanelTypeStat:       true,
}

// normalizeSQLPanel validates a logs_sql panel and drops the other sources'
// query fields, which it never reads; any other panel may not carry SQL.
func normalizeSQLPanel(panel *dashboards_models.Panel) error {
	if panel.EffectiveSource() != dashboards_models.PanelSourceLogsSQL {
		if panel.SQL != "" {
			return fmt.Errorf("sql is a setting of a logs_sql panel, not a %s one", panel.EffectiveSource())
		}
		return nil
	}
	if !sqlPanelTypes[panel.Type] {
		return fmt.Errorf("a logs_sql panel is a line, rankedList, donut, table or stat panel, not %s", panel.Type)
	}
	sql, err := logs_core.ValidateSavedSQL(panel.SQL)
	if err != nil {
		return err
	}
	panel.SQL = sql
	panel.Query, panel.PromQuery, panel.GroupField, panel.BucketMs = nil, "", "", 0
	panel.ServiceName, panel.OperationName, panel.RootSpansOnly, panel.Metric = "", "", false, ""
	return nil
}

// executeSQLPanelQuery runs the panel's SQL through SQL mode's executor (its
// sandbox, timeouts and row cap) over the request's window.
func (s *DashboardService) executeSQLPanelQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *dashboards_dto.PanelQueryRequestDTO,
) (*dashboards_dto.PanelQueryResponseDTO, error) {
	response, err := logs_core.GetLogCoreRepository().ExecuteSQLQueryContext(ctx, projectID,
		&logs_core.ExecuteSQLQueryRequestDTO{
			SQL:        request.Panel.SQL,
			TimeRange:  request.TimeRange,
			ScanBudget: config.GetEnv().QueryMaxScanBytes,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to execute panel query: %w", err)
	}
	return &dashboards_dto.PanelQueryResponseDTO{
		Buckets: []dashboards_dto.PanelBucketDTO{},
		Table: &dashboards_dto.PanelTableDTO{
			Columns: response.Columns, Rows: response.Rows, Truncated: response.Truncated,
		},
	}, nil
}
