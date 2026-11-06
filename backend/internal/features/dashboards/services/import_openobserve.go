package dashboards_services

import (
	"encoding/json"
	"fmt"

	dashboards_models "obzly/internal/features/dashboards/models"

	"github.com/google/uuid"
)

// importOpenObserveDashboard reads panels from "panels" or "tabs[].panels"
// (flattened into one list). Like the Grafana import, queries are not
// translated.
func importOpenObserveDashboard(
	rawJSON string,
) (title string, panels dashboards_models.Panels, warnings []string, err error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &doc); err != nil {
		return "", nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}

	title = stringOr(doc["title"], "Imported dashboard")

	rawPanels, _ := doc["panels"].([]any)
	if len(rawPanels) == 0 {
		if tabs, ok := doc["tabs"].([]any); ok {
			for _, tab := range tabs {
				tabMap, ok := tab.(map[string]any)
				if !ok {
					continue
				}
				if tabPanels, ok := tabMap["panels"].([]any); ok {
					rawPanels = append(rawPanels, tabPanels...)
				}
			}
		}
	}

	panels = convertOpenObservePanels(rawPanels)

	if len(panels) == 0 {
		warnings = append(warnings, "no panels found in the imported JSON")
	}
	warnings = append(
		warnings,
		"queries are not translated from OpenObserve - edit each panel to add one; "+
			"panels show all logs in range until then",
	)

	return title, panels, warnings, nil
}

func convertOpenObservePanels(rawPanels []any) dashboards_models.Panels {
	panels := dashboards_models.Panels{}

	for _, item := range rawPanels {
		panelMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		openObserveType, _ := panelMap["type"].(string)
		panels = append(panels, dashboards_models.Panel{
			ID:      uuid.NewString(),
			Title:   stringOr(panelMap["title"], "Untitled panel"),
			Type:    mapOpenObservePanelType(openObserveType),
			GridPos: extractGridPos(panelMap["layout"]),
		})
	}

	return panels
}

func mapOpenObservePanelType(openObserveType string) dashboards_models.PanelType {
	switch openObserveType {
	case "line", "area", "area-stacked", "scatter":
		return dashboards_models.PanelTypeLine
	case "bar", "h-bar", "stacked", "h-stacked":
		return dashboards_models.PanelTypeBar
	case "pie", "donut":
		return dashboards_models.PanelTypePie
	case "metric", "gauge":
		return dashboards_models.PanelTypeStat
	case "table":
		return dashboards_models.PanelTypeTable
	default:
		return dashboards_models.PanelTypeLine
	}
}
