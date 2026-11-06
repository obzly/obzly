package dashboards_services

import (
	"strings"

	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	"obzly/internal/util/attrnames"
)

// NormalizeDashboardNames rewrites a dashboard's attribute, label and metric
// names to the attrnames rule and its level filters to lowercase. Used on
// import and by the one-time migration of stored dashboards. Reports
// whether anything changed.
func NormalizeDashboardNames(dashboard *dashboards_models.Dashboard) bool {
	changed := false
	for t := range dashboard.Tabs {
		for p := range dashboard.Tabs[t].Panels {
			if normalizePanelNames(&dashboard.Tabs[t].Panels[p]) {
				changed = true
			}
		}
	}
	for v := range dashboard.Variables {
		variable := &dashboard.Variables[v]
		if normal := attrnames.Normalize(variable.MetricLabelName); normal != variable.MetricLabelName {
			variable.MetricLabelName = normal
			changed = true
		}
	}
	return changed
}

func normalizePanelNames(panel *dashboards_models.Panel) bool {
	changed := false
	set := func(target *string, value string) {
		if *target != value {
			*target = value
			changed = true
		}
	}
	if panel.PromQuery != "" {
		set(&panel.PromQuery, metrics_promql.NormalizePromQL(panel.PromQuery))
	}
	if panel.GroupField != "" {
		set(&panel.GroupField, attrnames.Normalize(panel.GroupField))
	}
	if panel.Metric != "" {
		set(&panel.Metric, attrnames.NormalizeMetricName(panel.Metric))
	}
	if panel.Query != nil && normalizeQueryNames(panel.Query) {
		changed = true
	}
	return changed
}

func normalizeQueryNames(node *logs_core.QueryNode) bool {
	changed := false
	if node.Condition != nil {
		field := attrnames.Normalize(node.Condition.Field)
		if field != node.Condition.Field {
			node.Condition.Field = field
			changed = true
		}
		if field == "level" {
			lowered := logs_core.LowercaseCondition(node.Condition)
			if !sameValue(lowered.Value, node.Condition.Value) {
				node.Condition.Value = lowered.Value
				changed = true
			}
		}
	}
	if node.Logic != nil {
		for i := range node.Logic.Children {
			if normalizeQueryNames(&node.Logic.Children[i]) {
				changed = true
			}
		}
	}
	return changed
}

func sameValue(a, b any) bool {
	return strings.TrimSpace(fmtValue(a)) == strings.TrimSpace(fmtValue(b))
}

func fmtValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case []string:
		return strings.Join(value, "\x00")
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i], _ = item.(string)
		}
		return strings.Join(parts, "\x00")
	default:
		return ""
	}
}
