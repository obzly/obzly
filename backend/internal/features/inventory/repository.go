package inventory

import (
	"context"
	"slices"
	"strings"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

func (r *repository) projectIDs() ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := storage.GetDb().Table("projects").Order("created_at").Pluck("id", &ids).Error
	return ids, err
}

func (r *repository) assets(ctx context.Context, projectID uuid.UUID) ([]Asset, error) {
	var assets []Asset
	err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Find(&assets).Error
	return assets, err
}

// save writes a build's result in one transaction: upserts of the assets
// that changed, last_seen of those only seen again, the changes, and the
// deletions past retention.
func (r *repository) save(ctx context.Context, assets, seen []*Asset, changes []Change, stale []uuid.UUID) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(stale) > 0 {
			for batch := range slices.Chunk(stale, 1000) {
				if err := tx.Where("id IN ?", batch).Delete(&Asset{}).Error; err != nil {
					return err
				}
			}
		}
		for batch := range slices.Chunk(seen, 1000) {
			if err := touchLastSeen(tx, batch); err != nil {
				return err
			}
		}
		if len(assets) > 0 {
			err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "project_id"}, {Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"kind", "name", "subtype", "parent_key", "node_key", "cluster", "namespace", "node",
					"attributes", "sources", "first_seen", "last_seen", "state", "health", "reasons", "metrics",
					"updated_at",
				}),
			}).Omit("id").CreateInBatches(assets, 500).Error
			if err != nil {
				return err
			}
		}
		if len(changes) > 0 {
			return tx.Omit("id").CreateInBatches(changes, 500).Error
		}
		return nil
	})
}

func (r *repository) deleteChangesBefore(before time.Time) error {
	return storage.GetDb().Where("at < ?", before).Delete(&Change{}).Error
}

func (r *repository) listAssets(projectIDs []uuid.UUID, filter assetFilter, limit int) ([]Asset, int64, error) {
	query := storage.GetDb().Model(&Asset{}).Where("project_id IN ?", projectIDs)
	if len(filter.kinds) > 0 {
		query = query.Where("kind IN ?", filter.kinds)
	}
	if len(filter.health) > 0 {
		query = query.Where("health IN ?", filter.health)
	}
	if len(filter.states) > 0 {
		query = query.Where("state IN ?", filter.states)
	}
	if filter.cluster != "" {
		query = query.Where("cluster = ?", filter.cluster)
	}
	if filter.namespace != "" {
		query = query.Where("namespace = ?", filter.namespace)
	}
	if filter.search != "" {
		like := "%" + escapeLike(filter.search) + "%"
		query = query.Where("(name ILIKE ? OR node ILIKE ? OR attributes::text ILIKE ?)", like, like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var assets []Asset
	err := query.Order("CASE health WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 WHEN 'healthy' THEN 2 " +
		"WHEN 'unknown' THEN 3 ELSE 4 END, kind, name").Limit(limit).Find(&assets).Error
	return assets, total, err
}

func (r *repository) asset(id uuid.UUID) (*Asset, error) {
	var asset Asset
	if err := storage.GetDb().Where("id = ?", id).First(&asset).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *repository) assetByKey(projectID uuid.UUID, key string) (*Asset, error) {
	var asset Asset
	err := storage.GetDb().Where("project_id = ? AND key = ?", projectID, key).First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *repository) children(projectID uuid.UUID, key string, limit int) ([]Asset, error) {
	var assets []Asset
	err := storage.GetDb().Where("project_id = ? AND (parent_key = ? OR node_key = ?)", projectID, key, key).
		Order("CASE health WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, name").
		Limit(limit).Find(&assets).Error
	return assets, err
}

func (r *repository) changes(projectIDs []uuid.UUID, assetKey string, since time.Time, limit int) ([]Change, error) {
	query := storage.GetDb().Where("project_id IN ? AND at >= ?", projectIDs, since)
	if assetKey != "" {
		query = query.Where("asset_key = ?", assetKey)
	}
	var changes []Change
	err := query.Order("at DESC").Limit(limit).Find(&changes).Error
	return changes, err
}

// summary counts assets by kind and health.
func (r *repository) summary(projectIDs []uuid.UUID) ([]summaryRow, error) {
	var rows []summaryRow
	err := storage.GetDb().Model(&Asset{}).
		Select("project_id, kind, health, state, count(*) AS count").
		Where("project_id IN ?", projectIDs).
		Group("project_id, kind, health, state").Scan(&rows).Error
	return rows, err
}

type summaryRow struct {
	ProjectID uuid.UUID
	Kind      string
	Health    string
	State     string
	Count     int64
}

// touchLastSeen moves last_seen of assets in one statement.
func touchLastSeen(tx *gorm.DB, assets []*Asset) error {
	ids := make([]string, len(assets))
	times := make([]string, len(assets))
	for i, a := range assets {
		ids[i] = a.ID.String()
		times[i] = `"` + a.LastSeen.UTC().Format(time.RFC3339Nano) + `"`
	}
	return tx.Exec(`UPDATE inventory_assets AS a SET last_seen = u.last_seen
		FROM unnest(?::uuid[], ?::timestamptz[]) AS u(id, last_seen) WHERE a.id = u.id`,
		"{"+strings.Join(ids, ",")+"}", "{"+strings.Join(times, ",")+"}").Error
}

func escapeLike(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
