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

// saveCounts replaces the project's counts of the hour: a later build in the hour wins.
func (r *repository) saveCounts(ctx context.Context, projectID uuid.UUID, hour time.Time, rows []countRow) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ? AND hour = ?", projectID, hour).Delete(&countRow{}).Error; err != nil {
			return err
		}
		return tx.CreateInBatches(rows, 500).Error
	})
}

func (r *repository) deleteCountsBefore(before time.Time) error {
	return storage.GetDb().Where("hour < ?", before).Delete(&countRow{}).Error
}

// counts sums the projects' hourly counts since, by hour, kind and health, oldest first.
func (r *repository) counts(projectIDs []uuid.UUID, since time.Time) ([]countRow, error) {
	var rows []countRow
	err := storage.GetDb().Model(&countRow{}).
		Select("hour, kind, health, sum(count) AS count").
		Where("project_id IN ? AND hour >= ?", projectIDs, since).
		Group("hour, kind, health").Order("hour").Scan(&rows).Error
	return rows, err
}

func (r *repository) listAssets(projectIDs []uuid.UUID, filter assetFilter, page assetPage) ([]Asset, int64, error) {
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
	if filter.parent != "" {
		query = query.Where("parent_key = ?", filter.parent)
	}
	if filter.roots {
		// An asset whose parent isn't in the inventory (any more) is a root too.
		query = query.Where("(parent_key = '' OR NOT EXISTS (SELECT 1 FROM inventory_assets AS p " +
			"WHERE p.project_id = inventory_assets.project_id AND p.key = inventory_assets.parent_key))")
	}
	if filter.search != "" {
		like := "%" + escapeLike(filter.search) + "%"
		searchProjects := filter.searchProjects
		if len(searchProjects) == 0 {
			searchProjects = []uuid.UUID{uuid.Nil}
		}
		query = query.Where("(name ILIKE ? OR node ILIKE ? OR project_id IN ? OR EXISTS "+
			"(SELECT 1 FROM jsonb_each_text(attributes) AS attr(k, v) WHERE attr.v ILIKE ?))",
			like, like, searchProjects, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var assets []Asset
	err := query.Order(orderBy(page.sort, page.descending)).Offset(page.offset).Limit(page.limit).
		Find(&assets).Error
	return assets, total, err
}

// childCounts counts the assets under each of keys, by project and key.
func (r *repository) childCounts(projectIDs []uuid.UUID, keys []string) (map[projectKey]int64, error) {
	counts := map[projectKey]int64{}
	for batch := range slices.Chunk(keys, 1000) {
		var rows []struct {
			ProjectID uuid.UUID
			ParentKey string
			Count     int64
		}
		err := storage.GetDb().Model(&Asset{}).Select("project_id, parent_key, count(*) AS count").
			Where("project_id IN ? AND parent_key IN ?", projectIDs, batch).
			Group("project_id, parent_key").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			counts[projectKey{row.ProjectID, row.ParentKey}] = row.Count
		}
	}
	return counts, nil
}

// assetsByKey reads the assets of projectIDs with one of keys; a key may exist in several
// projects, so callers match on project and key.
func (r *repository) assetsByKey(projectIDs []uuid.UUID, keys []string) ([]Asset, error) {
	var assets []Asset
	for batch := range slices.Chunk(keys, 1000) {
		var found []Asset
		if err := storage.GetDb().Where("project_id IN ? AND key IN ?", projectIDs, batch).
			Find(&found).Error; err != nil {
			return nil, err
		}
		assets = append(assets, found...)
	}
	return assets, nil
}

// idsByKey maps project and key to the asset's id.
func (r *repository) idsByKey(projectIDs []uuid.UUID, keys []string) (map[projectKey]uuid.UUID, error) {
	ids := map[projectKey]uuid.UUID{}
	for batch := range slices.Chunk(keys, 1000) {
		var rows []struct {
			ID        uuid.UUID
			ProjectID uuid.UUID
			Key       string
		}
		err := storage.GetDb().Model(&Asset{}).Select("id, project_id, key").
			Where("project_id IN ? AND key IN ?", projectIDs, batch).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			ids[projectKey{row.ProjectID, row.Key}] = row.ID
		}
	}
	return ids, nil
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

// regions counts the live assets with a cloud region, pods and containers aside (there are many
// of them and they sit in their node's region anyway).
func (r *repository) regions(projectIDs []uuid.UUID) ([]regionRow, error) {
	var rows []regionRow
	err := storage.GetDb().Model(&Asset{}).
		Select("COALESCE(attributes->>'cloud_provider', '') AS provider, attributes->>'cloud_region' AS region, "+
			"project_id, health, count(*) AS count").
		Where("project_id IN ? AND state <> ? AND kind NOT IN ? AND COALESCE(attributes->>'cloud_region', '') <> ''",
			projectIDs, StateTerminated, []string{KindPod, KindContainer}).
		Group("1, 2, project_id, health").Scan(&rows).Error
	return rows, err
}

func (r *repository) clusters(projectIDs []uuid.UUID) ([]string, error) {
	var names []string
	err := storage.GetDb().Model(&Asset{}).Distinct("cluster").
		Where("project_id IN ? AND kind = ?", projectIDs, KindCluster).Order("cluster").Pluck("cluster", &names).Error
	return names, err
}

type regionRow struct {
	Provider  string
	Region    string
	ProjectID uuid.UUID
	Health    string
	Count     int64
}

type projectKey struct {
	projectID uuid.UUID
	key       string
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

// orderBy sorts like the Assets table: ascending is how the table sorts a column first (worst
// health, newest, least CPU), descending reverses every key. Names compare bytewise (COLLATE "C") and
// the id breaks ties, so the UI can merge pages of several Sites in the same order.
func orderBy(sort string, descending bool) string {
	keys, ok := sortKeys[sort]
	if !ok {
		keys = sortKeys[SortHealth]
	}
	keys = append(slices.Clone(keys), sortKey{"name COLLATE \"C\"", true}, sortKey{"id", true})
	parts := make([]string, len(keys))
	for i, key := range keys {
		direction := "ASC"
		if key.ascending == descending {
			direction = "DESC"
		}
		parts[i] = key.expr + " " + direction
	}
	return strings.Join(parts, ", ")
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
