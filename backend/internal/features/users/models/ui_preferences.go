package users_models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Theme ids mirror frontend/src/shared/theme/themes.ts; a new theme needs both.
var (
	ThemeModes    = []string{"light", "dark", "system"}
	LightThemeIDs = []string{"emerald-light", "soft-light", "semi-dark"}
	DarkThemeIDs  = []string{"emerald-dark", "midnight", "black"}
)

const (
	DefaultThemeMode  = "system"
	DefaultLightTheme = "emerald-light"
	DefaultDarkTheme  = "emerald-dark"
)

var ErrInvalidThemePreferences = errors.New("invalid theme preferences")

type ThemePreferences struct {
	Mode             string `json:"mode"`
	Light            string `json:"light"`
	Dark             string `json:"dark"`
	ColorBlindCharts bool   `json:"colorBlindCharts"`
}

// UIPreferences is the users.ui_preferences JSONB column: per-user UI choices
// that follow the user to any browser.
type UIPreferences struct {
	Theme *ThemePreferences `json:"theme,omitempty"`
}

func (p *ThemePreferences) Validate() error {
	if err := ValidateThemeMode(p.Mode); err != nil {
		return err
	}
	return ValidateThemePair(p.Light, p.Dark)
}

func ValidateThemeMode(mode string) error {
	if !slices.Contains(ThemeModes, mode) {
		return fmt.Errorf("%w: mode must be one of light, dark, system", ErrInvalidThemePreferences)
	}
	return nil
}

func ValidateThemePair(light, dark string) error {
	if !slices.Contains(LightThemeIDs, light) {
		return fmt.Errorf("%w: unknown light theme %q", ErrInvalidThemePreferences, light)
	}
	if !slices.Contains(DarkThemeIDs, dark) {
		return fmt.Errorf("%w: unknown dark theme %q", ErrInvalidThemePreferences, dark)
	}
	return nil
}

// IsEmpty: a NULL column scans into an empty value, which is treated as "never saved".
func (p *UIPreferences) IsEmpty() bool {
	return p == nil || p.Theme == nil
}

func (p UIPreferences) Value() (driver.Value, error) {
	if p.Theme == nil {
		return nil, nil
	}
	return json.Marshal(p)
}

func (p *UIPreferences) Scan(src any) error {
	var raw []byte
	switch value := src.(type) {
	case nil:
		*p = UIPreferences{}
		return nil
	case []byte:
		raw = value
	case string:
		raw = []byte(value)
	default:
		return fmt.Errorf("unsupported ui_preferences type %T", src)
	}
	return json.Unmarshal(raw, p)
}
