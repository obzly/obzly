package dashboards_services

import dashboards_models "obzly/internal/features/dashboards/models"

// extractGridPos reads an imported {x,y,w,h} map leniently: bad fields get a
// default rather than failing the import, and values are clamped to our
// 12-column grid.
func extractGridPos(raw any) dashboards_models.GridPos {
	x, y, w, h := 0, 0, 12, 8

	if fields, ok := raw.(map[string]any); ok {
		if v := intFromAny(fields["x"]); v != 0 {
			x = v
		}
		if v := intFromAny(fields["y"]); v != 0 {
			y = v
		}
		if v := intFromAny(fields["w"]); v != 0 {
			w = v
		}
		if v := intFromAny(fields["h"]); v != 0 {
			h = v
		}
	}

	return dashboards_models.GridPos{
		X: clampInt(x, 0, 11),
		Y: clampInt(y, 0, 10_000),
		W: clampInt(w, 1, 12),
		H: clampInt(h, 1, 100),
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func stringOr(value any, fallback string) string {
	if s, ok := value.(string); ok && s != "" {
		return s
	}
	return fallback
}
