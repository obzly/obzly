package dashboards_services

import (
	"encoding/json"
	"fmt"

	dashboards_models "obzly/internal/features/dashboards/models"

	"github.com/google/uuid"
)

// importGrafanaDashboard imports title, panel titles, types and grid
// positions; queries are not translated (see ARCHITECTURE.md "Dashboards").
func importGrafanaDashboard(
	rawJSON string,
) (title string, panels dashboards_models.Panels, warnings []string, err error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &doc); err != nil {
		return "", nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// API and provisioning exports wrap the dashboard under "dashboard"; a UI
	// "Export as JSON" doesn't.
	root := doc
	if nested, ok := doc["dashboard"].(map[string]any); ok {
		root = nested
	}

	title = stringOr(root["title"], "Imported dashboard")

	rawPanels, _ := root["panels"].([]any)
	panels, skippedRows := convertGrafanaPanels(rawPanels)

	if len(panels) == 0 {
		warnings = append(warnings, "no panels found in the imported JSON")
	}
	if skippedRows > 0 {
		warnings = append(
			warnings,
			fmt.Sprintf("%d row/organizational panel(s) skipped (only chart panels are imported)", skippedRows),
		)
	}
	warnings = append(
		warnings,
		"queries are not translated from Grafana - edit each panel to add one; "+
			"panels show all logs in range until then",
	)

	return title, panels, warnings, nil
}

func convertGrafanaPanels(rawPanels []any) (dashboards_models.Panels, int) {
	panels := dashboards_models.Panels{}
	skippedRows := 0

	for _, item := range rawPanels {
		panelMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		grafanaType, _ := panelMap["type"].(string)
		if grafanaType == "row" {
			// A "row" panel is a collapsible section divider, not a chart -
			// nothing to import it as.
			skippedRows++
			continue
		}

		panels = append(panels, dashboards_models.Panel{
			ID:      uuid.NewString(),
			Title:   stringOr(panelMap["title"], "Untitled panel"),
			Type:    mapGrafanaPanelType(grafanaType),
			GridPos: extractGridPos(panelMap["gridPos"]),
		})
	}

	return panels, skippedRows
}

func mapGrafanaPanelType(grafanaType string) dashboards_models.PanelType {
	switch grafanaType {
	case "timeseries", "graph":
		return dashboards_models.PanelTypeLine
	case "barchart", "bargauge":
		return dashboards_models.PanelTypeBar
	case "piechart":
		return dashboards_models.PanelTypePie
	case "stat", "singlestat", "gauge":
		return dashboards_models.PanelTypeStat
	case "table", "table-old":
		return dashboards_models.PanelTypeTable
	default:
		return dashboards_models.PanelTypeLine
	}
}
