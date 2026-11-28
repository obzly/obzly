package broker

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LaneStore keeps each project's lane count per signal durably. Lane counts
// lived only in Valkey until a node-kill drill: Valkey came back empty, a
// busy project went back to one lane, and its shard built a 919-batch
// backlog that took about 20 minutes to drain.
type LaneStore interface {
	// LoadLanes is every project with two or more lanes, per signal.
	LoadLanes(ctx context.Context) (map[string]map[uuid.UUID]int, error)
	// SetLanes records project's lane count; 1 or less removes it.
	SetLanes(ctx context.Context, signal string, projectID uuid.UUID, lanes int) error
}

// PostgresLaneStore is the LaneStore of a Site: the ingest_lanes table.
type PostgresLaneStore struct {
	db *gorm.DB
}

type ingestLane struct {
	Signal    string    `gorm:"column:signal;primaryKey"`
	ProjectID uuid.UUID `gorm:"column:project_id;primaryKey"`
	Lanes     int       `gorm:"column:lanes"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// NewPostgresLaneStore keeps lane counts in db.
func NewPostgresLaneStore(db *gorm.DB) *PostgresLaneStore {
	return &PostgresLaneStore{db: db}
}

// LoadLanes implements LaneStore.
func (s *PostgresLaneStore) LoadLanes(ctx context.Context) (map[string]map[uuid.UUID]int, error) {
	var rows []ingestLane
	if err := s.db.WithContext(ctx).Table("ingest_lanes").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[string]map[uuid.UUID]int{}
	for _, row := range rows {
		if counts[row.Signal] == nil {
			counts[row.Signal] = map[uuid.UUID]int{}
		}
		counts[row.Signal][row.ProjectID] = min(row.Lanes, IngestShards)
	}
	return counts, nil
}

// SetLanes implements LaneStore.
func (s *PostgresLaneStore) SetLanes(ctx context.Context, signal string, projectID uuid.UUID, lanes int) error {
	db := s.db.WithContext(ctx).Table("ingest_lanes")
	if lanes < 2 {
		return db.Where("signal = ? AND project_id = ?", signal, projectID).Delete(&ingestLane{}).Error
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "signal"}, {Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"lanes", "updated_at"}),
	}).Create(&ingestLane{Signal: signal, ProjectID: projectID, Lanes: lanes, UpdatedAt: time.Now().UTC()}).Error
}
