package projects_models

import (
	"time"

	"github.com/google/uuid"
)

// Promoted field types: a string field is compared as text, a number one
// numerically (and supports range filters).
const (
	PromotedTypeString = "string"
	PromotedTypeNumber = "number"
)

// PromotedSlots are the typed log columns fields can be promoted into, in the order
// they're handed out. A retired field's slot is never reused, so a column means one
// field in every file.
var PromotedSlots = []string{
	"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8",
	"n1", "n2", "n3", "n4",
}

// PromotedField is a log field a project copies into its own typed column
// at ingest, so filtering and grouping by it skip parsing the fields JSON.
type PromotedField struct {
	ProjectID uuid.UUID  `json:"projectId"           gorm:"column:project_id;primaryKey"`
	Slot      string     `json:"slot"                gorm:"column:slot;primaryKey"`
	Field     string     `json:"field"               gorm:"column:field"`
	CreatedAt time.Time  `json:"createdAt"           gorm:"column:created_at"`
	RetiredAt *time.Time `json:"retiredAt,omitempty" gorm:"column:retired_at"`
}

func (PromotedField) TableName() string {
	return "project_promoted_fields"
}

// Type is the slot's type.
func (f PromotedField) Type() string {
	return SlotType(f.Slot)
}

// Column is the Parquet column the field is written to.
func (f PromotedField) Column() string {
	return "promoted_" + f.Slot
}

// Bit is the field's bit in a row's promoted_mask: set when the row was
// written with this promotion active.
func (f PromotedField) Bit() int64 {
	return SlotBit(f.Slot)
}

func SlotType(slot string) string {
	if len(slot) > 0 && slot[0] == 'n' {
		return PromotedTypeNumber
	}
	return PromotedTypeString
}

func SlotBit(slot string) int64 {
	for i, candidate := range PromotedSlots {
		if candidate == slot {
			return 1 << i
		}
	}
	return 0
}
