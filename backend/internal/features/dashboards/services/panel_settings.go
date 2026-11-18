package dashboards_services

import (
	"errors"
	"fmt"

	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
)

// normalizePanelSettings validates the v2 display settings (min/max,
// thresholds, higherIs) and drops an empty thresholds object. A setting on a
// type that doesn't use it is rejected rather than stored and ignored, so a
// saved panel never carries a setting its editor can't show.
func normalizePanelSettings(panel *dashboards_models.Panel) error {
	if !panel.HigherIs.IsValid() {
		return fmt.Errorf("invalid panel higherIs %q", panel.HigherIs)
	}
	if panel.HigherIs != dashboards_models.PanelHigherIsUnset && !panel.Type.UsesHigherIs() {
		return fmt.Errorf("higherIs is not a setting of a %s panel", panel.Type)
	}

	if (panel.Min != nil || panel.Max != nil) && !panel.Type.UsesRange() {
		return fmt.Errorf("min and max are not settings of a %s panel", panel.Type)
	}
	if panel.Min != nil && panel.Max != nil && *panel.Min >= *panel.Max {
		return errors.New("a panel's min must be less than its max")
	}

	if panel.Thresholds != nil && panel.Thresholds.Warn == nil && panel.Thresholds.Critical == nil {
		panel.Thresholds = nil
	}
	if panel.Thresholds == nil {
		return nil
	}
	if !panel.Type.UsesThresholds() {
		return fmt.Errorf("thresholds are not a setting of a %s panel", panel.Type)
	}
	warn, critical := panel.Thresholds.Warn, panel.Thresholds.Critical
	if warn != nil && critical != nil {
		if panel.HigherIs == dashboards_models.PanelHigherIsGood && *warn < *critical {
			return errors.New("when higher is good, the warn threshold must be at or above the critical one")
		}
		if panel.HigherIs != dashboards_models.PanelHigherIsGood && *warn > *critical {
			return errors.New("the warn threshold must be at or below the critical one")
		}
	}
	return nil
}

// validatePanelLogOptions checks a saved panel's measure and top groups:
// logs panels only, and never log bands (no panel kind draws them).
func validatePanelLogOptions(panel dashboards_models.Panel) error {
	if panel.GroupBands != "" {
		return errors.New("groupBands is only for previews; a saved panel can't use it")
	}
	usesOptions := !panel.Measure.IsCount() || panel.MeasureField != "" || panel.TopGroups != 0
	if !usesOptions {
		return nil
	}
	if panel.EffectiveSource() != dashboards_models.PanelSourceLogs {
		return fmt.Errorf("measure and topGroups are settings of a logs panel, not a %s one", panel.EffectiveSource())
	}
	request := logs_core.AggregationRequestDTO{
		GroupField:   panel.GroupField,
		Measure:      panel.Measure,
		MeasureField: panel.MeasureField,
		TopGroups:    panel.TopGroups,
	}
	if err := request.ValidateOptions(); err != nil {
		return err
	}
	return nil
}
