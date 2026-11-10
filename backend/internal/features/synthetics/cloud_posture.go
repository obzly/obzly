package synthetics

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"

	"obzly/internal/synthetics/spec"
)

// PostureConfig is a connection's posture settings in the posture_settings column (NULL without
// the posture use).
type PostureConfig struct {
	Settings *spec.PostureSettings
}

func (c PostureConfig) Value() (driver.Value, error) {
	if c.Settings == nil {
		return nil, nil
	}
	return jsonValue(c.Settings)
}

func (c *PostureConfig) Scan(value any) error {
	c.Settings = nil
	if value == nil {
		return nil
	}
	var settings spec.PostureSettings
	if err := scanJSON(value, &settings); err != nil {
		return err
	}
	c.Settings = &settings
	return nil
}

// postureSettingsFrom checks the posture use's settings for connection c (nil without the use).
func postureSettingsFrom(c *CloudConnection, requested *spec.PostureSettings) (*spec.PostureSettings, error) {
	if !slices.Contains(c.ScanTypes, spec.UsePosture) {
		return nil, nil
	}
	if requested == nil {
		requested = &spec.PostureSettings{}
	}
	settings := &spec.PostureSettings{
		Source: orDefault(requested.Source, spec.PostureSourceAuto), RefreshHours: requested.RefreshHours,
	}
	switch settings.Source {
	case spec.PostureSourceAuto, spec.PostureSourceNative, spec.PostureSourceBaseline:
	default:
		return nil, errors.New("posture source must be auto, native or baseline")
	}
	if settings.RefreshHours == 0 {
		settings.RefreshHours = spec.DefaultPostureRefreshHours
	}
	if settings.RefreshHours < spec.MinPostureRefreshHours || settings.RefreshHours > spec.MaxPostureRefreshHours {
		return nil, fmt.Errorf("posture refresh must be every %d to %d hours", spec.MinPostureRefreshHours,
			spec.MaxPostureRefreshHours)
	}
	return settings, nil
}

// boundedPosture keeps a reported posture summary small.
func boundedPosture(info *spec.PostureScanInfo) *spec.PostureScanInfo {
	if info == nil {
		return nil
	}
	bounded := *info
	limit := func(scopes []string) []string {
		if len(scopes) > maxScanRegions {
			scopes = scopes[:maxScanRegions]
		}
		out := make([]string, len(scopes))
		for i, scope := range scopes {
			if len(scope) > maxScanText {
				scope = scope[:maxScanText]
			}
			out[i] = scope
		}
		return out
	}
	bounded.Native, bounded.Baseline, bounded.NotCovered = limit(info.Native), limit(info.Baseline),
		limit(info.NotCovered)
	return &bounded
}
