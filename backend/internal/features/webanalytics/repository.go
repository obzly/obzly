package webanalytics

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

// bucketSelection is which stored buckets a query reads: whole days and
// single hours.
type bucketSelection struct {
	days  []time.Time
	hours []time.Time
}

// keyFilter narrows stored rows to sources (nil: all) and a host ("": all).
type keyFilter struct {
	sources []string
	host    string
}

// rankedKey is one top-list row summed over a selection.
type rankedKey struct {
	Dim       int16
	Key       string
	Hits      int64
	Bytes     int64
	Errors5xx int64
	Referred  int64
	LastSeen  *time.Time
	Extra     string
}

// storageStats is what a project's rollups hold.
type storageStats struct {
	Hours int64
	Bytes int64
}

func (s bucketSelection) empty() bool { return len(s.days) == 0 && len(s.hours) == 0 }

func (r *repository) settings(ctx context.Context, projectID uuid.UUID) (*Settings, error) {
	var settings Settings
	err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Take(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &settings, err
}

// ensureSettings returns the project's settings, creating the defaults
// (and so starting its rollups) on first use.
func (r *repository) ensureSettings(ctx context.Context, projectID uuid.UUID) (*Settings, error) {
	existing, err := r.settings(ctx, projectID)
	if err != nil || existing != nil {
		return existing, err
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	settings := &Settings{
		ProjectID:   projectID,
		IPMode:      ipModeTruncate,
		HashSalt:    salt,
		ExcludeBots: true,
		StaticExtensions: staticExtensionsText(
			defaultStaticExtensions,
		),
		GeoEnabled: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	err = storage.GetDb().WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(settings).Error
	if err != nil {
		return nil, err
	}
	return r.settings(ctx, projectID)
}

func (r *repository) saveSettings(ctx context.Context, settings *Settings) error {
	return storage.GetDb().WithContext(ctx).Save(settings).Error
}

// activeProjects are the projects whose hours the worker summarises.
func (r *repository) activeProjects(ctx context.Context) ([]Settings, error) {
	var list []Settings
	err := storage.GetDb().WithContext(ctx).
		Where("project_id IN (SELECT id FROM projects)").Order("created_at").Find(&list).Error
	return list, err
}

// builds are the project's build markers of granularity in [from, to).
func (r *repository) builds(ctx context.Context, projectID uuid.UUID, granularity string, from, to time.Time,
) (map[time.Time]Build, error) {
	var list []Build
	err := storage.GetDb().WithContext(ctx).
		Where("project_id = ? AND granularity = ? AND bucket_start >= ? AND bucket_start < ?",
			projectID, granularity, from, to).Find(&list).Error
	result := make(map[time.Time]Build, len(list))
	for _, build := range list {
		result[build.BucketStart.UTC()] = build
	}
	return result, err
}

// lastBuild is the project's most recent hourly build, nil before any.
func (r *repository) lastBuild(ctx context.Context, projectID uuid.UUID) (*Build, error) {
	var build Build
	err := storage.GetDb().WithContext(ctx).
		Where("project_id = ? AND granularity = ?", projectID, granularityHour).
		Order("built_at DESC").Take(&build).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &build, err
}

// saveBucket replaces a bucket's rows and marks it built, in one
// transaction (a rebuild wins).
func (r *repository) saveBucket(ctx context.Context, build Build, totals []TotalsRow, keys []KeyRow) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		where := "project_id = ? AND granularity = ? AND bucket_start = ?"
		if err := tx.Where(where, build.ProjectID, build.Granularity, build.BucketStart).
			Delete(&TotalsRow{}).
			Error; err != nil {
			return err
		}
		if err := tx.Where(where, build.ProjectID, build.Granularity, build.BucketStart).
			Delete(&KeyRow{}).
			Error; err != nil {
			return err
		}
		if len(totals) > 0 {
			if err := tx.CreateInBatches(totals, 500).Error; err != nil {
				return err
			}
		}
		if len(keys) > 0 {
			if err := tx.CreateInBatches(keys, 1000).Error; err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&build).Error
	})
}

// totals reads the stored totals of a selection.
func (r *repository) totals(ctx context.Context, projectID uuid.UUID, selection bucketSelection, filter keyFilter,
) ([]TotalsRow, error) {
	if selection.empty() {
		return nil, nil
	}
	var rows []TotalsRow
	err := r.scoped(ctx, projectID, selection, filter).Find(&rows).Error
	return rows, err
}

// hourlyRequests sums requests per stored hour in [from, to), for the time
// distribution.
func (r *repository) hourlyRequests(ctx context.Context, projectID uuid.UUID, from, to time.Time, filter keyFilter,
) (map[time.Time]int64, error) {
	var rows []struct {
		BucketStart time.Time
		Requests    int64
	}
	query := storage.GetDb().WithContext(ctx).Model(&TotalsRow{}).
		Select("bucket_start, sum(requests) AS requests").
		Where("project_id = ? AND granularity = ? AND bucket_start >= ? AND bucket_start < ?",
			projectID, granularityHour, from, to)
	err := applyFilter(query, filter).Group("bucket_start").Scan(&rows).Error
	result := make(map[time.Time]int64, len(rows))
	for _, row := range rows {
		result[row.BucketStart.UTC()] = row.Requests
	}
	return result, err
}

// rankedKeys sums a selection's top-list rows and keeps each list's limit
// busiest keys.
func (r *repository) rankedKeys(ctx context.Context, projectID uuid.UUID, selection bucketSelection,
	filter keyFilter, limit int) ([]rankedKey, error) {
	if selection.empty() {
		return nil, nil
	}
	inner := r.scopedKeys(ctx, projectID, selection, filter).
		Select("dim, key, sum(hits) AS hits, sum(bytes) AS bytes, sum(errors_5xx) AS errors5xx, " +
			"sum(referred) AS referred, max(last_seen) AS last_seen, max(extra) AS extra, " +
			"row_number() OVER (PARTITION BY dim ORDER BY sum(hits) DESC, key) AS rn").
		Group("dim, key")
	var rows []rankedKey
	err := storage.GetDb().WithContext(ctx).Table("(?) AS ranked", inner).
		Select("dim, key, hits, bytes, errors5xx AS errors5xx, referred, last_seen, extra").
		Where("rn <= ?", limit).Scan(&rows).Error
	return rows, err
}

// keySketches reads the visitor sketches of the given rows of a selection.
func (r *repository) keySketches(ctx context.Context, projectID uuid.UUID, selection bucketSelection,
	filter keyFilter, dims []int16, keys []string) ([]KeyRow, error) {
	if selection.empty() || len(keys) == 0 {
		return nil, nil
	}
	var rows []KeyRow
	err := r.scopedKeys(ctx, projectID, selection, filter).
		Select("dim, key, visitors_hll").
		Where("visitors_hll IS NOT NULL AND (dim, key) IN (SELECT * FROM unnest(?::smallint[], ?::text[]))",
			int16Array(dims), textArray(keys)).
		Find(&rows).Error
	return rows, err
}

// keysOfBucket reads a stored bucket's top lists (to build a day from its hours).
func (r *repository) keysOfBuckets(ctx context.Context, projectID uuid.UUID, granularity string,
	buckets []time.Time) ([]KeyRow, error) {
	var rows []KeyRow
	err := storage.GetDb().WithContext(ctx).
		Where("project_id = ? AND granularity = ? AND bucket_start IN ?", projectID, granularity, buckets).
		Find(&rows).Error
	return rows, err
}

func (r *repository) totalsOfBuckets(ctx context.Context, projectID uuid.UUID, granularity string,
	buckets []time.Time) ([]TotalsRow, error) {
	var rows []TotalsRow
	err := storage.GetDb().WithContext(ctx).
		Where("project_id = ? AND granularity = ? AND bucket_start IN ?", projectID, granularity, buckets).
		Find(&rows).Error
	return rows, err
}

func (r *repository) sources(ctx context.Context, projectID uuid.UUID) ([]Source, error) {
	var list []Source
	err := storage.GetDb().WithContext(ctx).Where("project_id = ?", projectID).Order("source_key").Find(&list).Error
	return list, err
}

// upsertSources records sources seen by a scan; the name and merge set in
// settings are kept.
func (r *repository) upsertSources(ctx context.Context, list []Source) error {
	if len(list) == 0 {
		return nil
	}
	return storage.GetDb().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}, {Name: "source_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"identified_by": gorm.Expr("excluded.identified_by"),
			"identity":      gorm.Expr("excluded.identity"),
			"format": gorm.Expr(
				"CASE WHEN excluded.format <> '' THEN excluded.format ELSE web_analytics_sources.format END",
			),
			"first_seen": gorm.Expr("LEAST(web_analytics_sources.first_seen, excluded.first_seen)"),
			"last_seen":  gorm.Expr("GREATEST(web_analytics_sources.last_seen, excluded.last_seen)"),
		}),
	}).Omit("name", "merged_into").Create(&list).Error
}

func (r *repository) saveSourceNames(ctx context.Context, projectID uuid.UUID, list []Source) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, source := range list {
			err := tx.Model(&Source{}).Where("project_id = ? AND source_key = ?", projectID, source.SourceKey).
				Updates(map[string]any{"name": source.Name, "merged_into": source.MergedInto}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// deleteBefore drops a project's rollups older than before.
func (r *repository) deleteBefore(ctx context.Context, projectID uuid.UUID, before time.Time) error {
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&KeyRow{}, &TotalsRow{}, &Build{}} {
			if err := tx.Where("project_id = ? AND bucket_start < ?", projectID, before).
				Delete(model).
				Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// deleteHoursCoveredBy drops hourly rows older than before whose day has
// been built: ranges that old read the day.
func (r *repository) deleteHoursCoveredBy(ctx context.Context, projectID uuid.UUID, before time.Time) error {
	covered := "project_id = ? AND granularity = ? AND bucket_start < ? AND date_trunc('day', bucket_start AT TIME ZONE 'UTC') " +
		"IN (SELECT bucket_start AT TIME ZONE 'UTC' FROM web_analytics_builds WHERE project_id = ? AND granularity = ?)"
	return storage.GetDb().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&KeyRow{}, &TotalsRow{}, &Build{}} {
			err := tx.Where(covered, projectID, granularityHour, before, projectID, granularityDay).Delete(model).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *repository) storage(ctx context.Context, projectID uuid.UUID) (storageStats, error) {
	var stats storageStats
	err := storage.GetDb().WithContext(ctx).Raw(`SELECT
		(SELECT count(*) FROM web_analytics_builds WHERE project_id = ? AND granularity = 'h') AS hours,
		(SELECT coalesce(sum(coalesce(octet_length(visitors_hll), 0) + octet_length(key) + 80), 0)
			FROM web_analytics_keys WHERE project_id = ?) +
		(SELECT coalesce(sum(coalesce(octet_length(visitors_hll), 0) + octet_length(format) + 200), 0)
			FROM web_analytics_totals WHERE project_id = ?) AS bytes`,
		projectID, projectID, projectID).Scan(&stats).Error
	return stats, err
}

func (r *repository) scoped(ctx context.Context, projectID uuid.UUID, selection bucketSelection, filter keyFilter,
) *gorm.DB {
	return applyFilter(applySelection(storage.GetDb().WithContext(ctx).Model(&TotalsRow{}).
		Where("project_id = ?", projectID), selection), filter)
}

func (r *repository) scopedKeys(ctx context.Context, projectID uuid.UUID, selection bucketSelection,
	filter keyFilter) *gorm.DB {
	return applyFilter(applySelection(storage.GetDb().WithContext(ctx).Model(&KeyRow{}).
		Where("project_id = ?", projectID), selection), filter)
}

func applySelection(query *gorm.DB, selection bucketSelection) *gorm.DB {
	days := selection.days
	if len(days) == 0 {
		days = []time.Time{{}}
	}
	hours := selection.hours
	if len(hours) == 0 {
		hours = []time.Time{{}}
	}
	return query.Where("((granularity = 'd' AND bucket_start IN ?) OR (granularity = 'h' AND bucket_start IN ?))",
		days, hours)
}

func applyFilter(query *gorm.DB, filter keyFilter) *gorm.DB {
	if filter.sources != nil {
		sources := filter.sources
		if len(sources) == 0 {
			sources = []string{""}
		}
		query = query.Where("source_key IN ?", sources)
	}
	if filter.host != "" {
		query = query.Where("host = ?", filter.host)
	}
	return query
}

// int16Array and textArray render Postgres array literals for unnest.
func int16Array(values []int16) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(int(value))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func textArray(values []string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		value = strings.ReplaceAll(value, "\x00", "")
		value = strings.ReplaceAll(value, `\`, `\\`)
		parts[i] = `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}
