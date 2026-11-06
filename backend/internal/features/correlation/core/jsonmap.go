package correlation_core

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap is a map stored as one JSONB column (hand-written Value/Scan, no
// gorm.io/datatypes dependency).
type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSONMap: %w", err)
	}
	return string(encoded), nil
}

func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = JSONMap{}
		return nil
	}

	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("unsupported type %T for JSONMap.Scan", value)
	}

	if len(raw) == 0 {
		*m = JSONMap{}
		return nil
	}

	var decoded JSONMap
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal JSONMap: %w", err)
	}
	*m = decoded
	return nil
}
