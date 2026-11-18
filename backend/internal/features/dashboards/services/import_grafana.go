package dashboards_services

import (
	"encoding/json"
	"fmt"
	"strings"

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

		panel := dashboards_models.Panel{
			ID:      uuid.NewString(),
			Title:   stringOr(panelMap["title"], "Untitled panel"),
			Type:    mapGrafanaPanelType(grafanaType, panelMap),
			GridPos: extractGridPos(panelMap["gridPos"]),
		}
		applyGrafanaFieldDefaults(&panel, panelMap)
		panels = append(panels, panel)
	}

	return panels, skippedRows
}

// mapGrafanaPanelType maps onto a v2 kind only where it is a clean match:
// bar gauges are a ranked list, a donut-style pie is a donut, a stat with an
// area graph is a stat with trend.
func mapGrafanaPanelType(grafanaType string, panelMap map[string]any) dashboards_models.PanelType {
	options, _ := panelMap["options"].(map[string]any)

	switch grafanaType {
	case "timeseries", "graph":
		return dashboards_models.PanelTypeLine
	case "barchart":
		return dashboards_models.PanelTypeBar
	case "bargauge":
		return dashboards_models.PanelTypeRankedList
	case "piechart":
		if options["pieType"] == "donut" {
			return dashboards_models.PanelTypeDonut
		}
		return dashboards_models.PanelTypePie
	case "stat":
		if options["graphMode"] == "area" {
			return dashboards_models.PanelTypeStatTrend
		}
		return dashboards_models.PanelTypeStat
	case "singlestat":
		if sparkline, ok := panelMap["sparkline"].(map[string]any); ok && sparkline["show"] == true {
			return dashboards_models.PanelTypeStatTrend
		}
		return dashboards_models.PanelTypeStat
	case "gauge":
		return dashboards_models.PanelTypeGauge
	case "status-history", "state-timeline":
		return dashboards_models.PanelTypeStatusStrip
	case "table", "table-old":
		return dashboards_models.PanelTypeTable
	default:
		return dashboards_models.PanelTypeLine
	}
}

// applyGrafanaFieldDefaults carries over fieldConfig.defaults where Obzly has
// the same setting: unit, gauge min/max and absolute thresholds. A setting
// that wouldn't pass validation is dropped rather than failing the import.
func applyGrafanaFieldDefaults(panel *dashboards_models.Panel, panelMap map[string]any) {
	fieldConfig, _ := panelMap["fieldConfig"].(map[string]any)
	defaults, _ := fieldConfig["defaults"].(map[string]any)
	if defaults == nil {
		return
	}

	switch defaults["unit"] {
	case "percentunit":
		panel.Unit = dashboards_models.PanelUnitPercent
	case "bytes", "decbytes":
		panel.Unit = dashboards_models.PanelUnitBytes
	}

	if panel.Type.UsesRange() {
		panel.Min = floatPtrFromAny(defaults["min"])
		panel.Max = floatPtrFromAny(defaults["max"])
	}
	if panel.Type.UsesThresholds() {
		panel.Thresholds, panel.HigherIs = grafanaThresholds(defaults["thresholds"])
	}

	if normalizePanelSettings(panel) != nil {
		panel.Min, panel.Max, panel.Thresholds, panel.HigherIs = nil, nil, nil, dashboards_models.PanelHigherIsUnset
	}
}

// grafanaThresholds reads absolute threshold steps whose colours say which
// is warn and which is critical: a green base rising to orange/yellow then
// red (higher is bad), or a red base rising to green (higher is good).
// Anything else (percentage mode, custom colours, more steps) is skipped.
func grafanaThresholds(raw any) (*dashboards_models.PanelThresholds, dashboards_models.PanelHigherIs) {
	thresholds, _ := raw.(map[string]any)
	if mode, ok := thresholds["mode"].(string); ok && mode != "absolute" {
		return nil, dashboards_models.PanelHigherIsUnset
	}
	steps, _ := thresholds["steps"].([]any)
	if len(steps) < 2 || len(steps) > 3 {
		return nil, dashboards_models.PanelHigherIsUnset
	}

	levels := make([]string, len(steps))
	values := make([]*float64, len(steps))
	for i, step := range steps {
		stepMap, _ := step.(map[string]any)
		color, _ := stepMap["color"].(string)
		levels[i] = grafanaColorLevel(color)
		values[i] = floatPtrFromAny(stepMap["value"])
		if i > 0 && values[i] == nil {
			return nil, dashboards_models.PanelHigherIsUnset
		}
	}

	pattern := strings.Join(levels, ",")
	switch pattern {
	case "ok,warn":
		return &dashboards_models.PanelThresholds{Warn: values[1]}, dashboards_models.PanelHigherIsUnset
	case "ok,critical":
		return &dashboards_models.PanelThresholds{Critical: values[1]}, dashboards_models.PanelHigherIsUnset
	case "ok,warn,critical":
		return &dashboards_models.PanelThresholds{Warn: values[1], Critical: values[2]},
			dashboards_models.PanelHigherIsUnset
	case "critical,ok":
		return &dashboards_models.PanelThresholds{Critical: values[1]}, dashboards_models.PanelHigherIsGood
	case "warn,ok":
		return &dashboards_models.PanelThresholds{Warn: values[1]}, dashboards_models.PanelHigherIsGood
	case "critical,warn,ok":
		return &dashboards_models.PanelThresholds{Warn: values[2], Critical: values[1]},
			dashboards_models.PanelHigherIsGood
	default:
		return nil, dashboards_models.PanelHigherIsUnset
	}
}

func grafanaColorLevel(color string) string {
	color = strings.ToLower(color)
	switch {
	case strings.Contains(color, "red"):
		return "critical"
	case strings.Contains(color, "orange"), strings.Contains(color, "yellow"):
		return "warn"
	case strings.Contains(color, "green"):
		return "ok"
	default:
		return "unknown"
	}
}

func floatPtrFromAny(value any) *float64 {
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	return &number
}
