package synthetics

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

func Test_ApplyImport_MonitorChangedSinceThePlan_ConflictsAndAppliesNothing(t *testing.T) {
	db := storage.GetDb()
	require.NoError(t, db.Exec("DELETE FROM cloud_connections").Error)
	require.NoError(t, db.Exec("DELETE FROM synthetic_checks").Error)
	require.NoError(t, db.Exec("DELETE FROM synthetic_destinations").Error)
	now := time.Now().UTC()
	destination := &Destination{
		ID: uuid.New(), Name: "results", OTLPURL: "https://site-a.example.com", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(destination).Error)
	monitor := func(name string, version int) *Check {
		return &Check{
			ID: uuid.New(), Name: name, Type: spec.TypeTCP, Enabled: true, IntervalSeconds: 60,
			Locations: Locations{}, DestinationID: destination.ID, ParentIDs: UUIDs{},
			Definition: DefinitionJSON(spec.Definition{Type: spec.TypeTCP, Target: "db.example.com:5432"}),
			Version:    version, CreatedAt: now, UpdatedAt: now,
		}
	}
	// Saved twice since the import was planned from version 1.
	saved := monitor("Orders DB", 3)
	require.NoError(t, db.Create(saved).Error)
	planned := *saved
	planned.Name, planned.Version = "Orders database", 2
	added := monitor("Payments DB", 1)

	err := (&repository{}).applyImport([]*Check{added}, []*Check{&planned}, nil, maintenancePlan{})

	require.ErrorIs(t, err, ErrConflict)
	var names []string
	require.NoError(t, db.Model(&Check{}).Order("name").Pluck("name", &names).Error)
	require.Equal(t, []string{"Orders DB"}, names, "nothing from the import was applied")
}
