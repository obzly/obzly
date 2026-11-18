package inventory

import (
	"context"
	"database/sql/driver"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
)

// buildEvery and buildTask: variables so a test can shorten the interval and
// keep its Valkey keys apart.
var (
	buildEvery = 5 * time.Minute
	buildTask  = "inventory-build"
)

const (
	// projectTimeout bounds one project's build.
	projectTimeout = 2 * time.Minute
	// parallelProjects: projects built at once.
	parallelProjects = 4
	keepChanges      = 7 * 24 * time.Hour
	keepCounts       = 30 * 24 * time.Hour
)

// Builder rebuilds every project's inventory every 5 minutes, on one worker
// replica per interval (leaderlock.RunAtMostEvery): with RunExclusive alone
// every worker rebuilt it every 5 minutes, each running every project's
// PromQL on its own metrics DuckDB.
type Builder struct {
	repo      *repository
	collector collector
	log       *slog.Logger
	now       func() time.Time
}

// Start runs the build loop until ctx ends.
func (b *Builder) Start(ctx context.Context) {
	go func() {
		// Let the rest of the worker start first.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if _, err := b.buildIfDue(ctx); err != nil {
				b.log.Error("inventory build failed", "error", err)
			}
			timer.Reset(buildEvery)
		}
	}()
}

// BuildAll rebuilds every project's inventory.
func (b *Builder) BuildAll(ctx context.Context) error {
	projects, err := b.repo.projectIDs()
	if err != nil {
		return err
	}
	work := make(chan uuid.UUID)
	var wg sync.WaitGroup
	for range parallelProjects {
		wg.Go(func() {
			for projectID := range work {
				projectCtx, cancel := context.WithTimeout(ctx, projectTimeout)
				if err := b.Build(projectCtx, projectID); err != nil {
					b.log.Warn("inventory build failed for project", "projectId", projectID, "error", err)
				}
				cancel()
			}
		})
	}
	for _, projectID := range projects {
		select {
		case work <- projectID:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if err := b.repo.deleteCountsBefore(b.now().Add(-keepCounts)); err != nil {
		return err
	}
	return b.repo.deleteChangesBefore(b.now().Add(-keepChanges))
}

// Build rebuilds one project's inventory now.
func (b *Builder) Build(ctx context.Context, projectID uuid.UUID) error {
	now := b.now()
	snap, err := b.collector.collect(ctx, projectID, now)
	if err != nil {
		return err
	}
	existing, err := b.repo.assets(ctx, projectID)
	if err != nil {
		return err
	}
	if len(existing) == 0 && len(snap.series) == 0 && len(snap.services) == 0 {
		return nil
	}
	assets, changes, stale := reconcile(projectID, existing, observe(snap), snap.alerts, now, retention())
	limit := config.GetEnv().InventoryMaxAssetsPerProject
	if len(assets) > limit {
		b.log.Warn("inventory: project has more assets than INVENTORY_MAX_ASSETS_PER_PROJECT; "+
			"the least recently seen are dropped", "projectId", projectID, "assets", len(assets), "limit", limit)
		assets, changes, stale = capAssets(assets, changes, stale, limit)
	}
	changed, seen := splitUnchanged(existing, assets)
	if err := b.repo.save(ctx, changed, seen, changes, stale); err != nil {
		return err
	}
	hour := now.Truncate(time.Hour)
	return b.repo.saveCounts(ctx, projectID, hour, countsOf(projectID, hour, assets))
}

func retention() time.Duration {
	days := config.GetEnv().InventoryRetentionDays
	if days <= 0 {
		days = 30
	}
	return time.Duration(days) * 24 * time.Hour
}

// countsOf is the hour's count rows: per kind and health, and the total under kind "".
func countsOf(projectID uuid.UUID, hour time.Time, assets []*Asset) []countRow {
	type key struct{ kind, health string }
	counts := map[key]int64{}
	for _, a := range assets {
		counts[key{a.Kind, a.Health}]++
	}
	rows := []countRow{{ProjectID: projectID, Hour: hour, Count: int64(len(assets))}}
	for k, count := range counts {
		rows = append(rows, countRow{ProjectID: projectID, Hour: hour, Kind: k.kind, Health: k.health, Count: count})
	}
	return rows
}

// capAssets keeps the limit most recently seen assets: the rest are
// deleted if stored, and their changes dropped.
func capAssets(assets []*Asset, changes []Change, stale []uuid.UUID, limit int) ([]*Asset, []Change, []uuid.UUID) {
	sorted := slices.Clone(assets)
	slices.SortStableFunc(sorted, func(a, b *Asset) int {
		if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	dropped := map[string]bool{}
	for _, a := range sorted[limit:] {
		dropped[a.Key] = true
		if a.ID != uuid.Nil {
			stale = append(stale, a.ID)
		}
	}
	kept := slices.DeleteFunc(slices.Clone(assets), func(a *Asset) bool { return dropped[a.Key] })
	changes = slices.DeleteFunc(changes, func(c Change) bool { return dropped[c.AssetKey] })
	return kept, changes, stale
}

// splitUnchanged separates the assets to write from those whose stored
// row only needs last_seen moved (or nothing at all), so a steady
// inventory isn't rewritten on every build.
func splitUnchanged(existing []Asset, assets []*Asset) (changed []*Asset, seen []*Asset) {
	previous := make(map[string]*Asset, len(existing))
	for i := range existing {
		previous[existing[i].Key] = &existing[i]
	}
	for _, a := range assets {
		before, ok := previous[a.Key]
		switch {
		case !ok || a.ID != before.ID || !samePersisted(before, a):
			changed = append(changed, a)
		case !sameTime(before.LastSeen, a.LastSeen):
			seen = append(seen, a)
		}
	}
	return changed, seen
}

// samePersisted compares every stored field but last_seen and updated_at.
func samePersisted(a, b *Asset) bool {
	if a.ProjectID != b.ProjectID || a.Kind != b.Kind || a.Name != b.Name || a.Subtype != b.Subtype ||
		a.ParentKey != b.ParentKey || a.NodeKey != b.NodeKey || a.Cluster != b.Cluster ||
		a.Namespace != b.Namespace || a.Node != b.Node || a.State != b.State || a.Health != b.Health ||
		!sameTime(a.FirstSeen, b.FirstSeen) {
		return false
	}
	for _, pair := range [][2]driver.Valuer{
		{a.Attributes, b.Attributes}, {a.Sources, b.Sources}, {a.Reasons, b.Reasons}, {a.Metrics, b.Metrics},
	} {
		left, errLeft := pair[0].Value()
		right, errRight := pair[1].Value()
		if errLeft != nil || errRight != nil || left != right {
			return false
		}
	}
	return true
}

// sameTime: equal at Postgres' microsecond precision.
func sameTime(a, b time.Time) bool { return a.Sub(b).Abs() < time.Microsecond }

// buildIfDue builds every project unless another replica built them within
// buildEvery or is building them now; ran says whether this replica built.
func (b *Builder) buildIfDue(ctx context.Context) (bool, error) {
	return leaderlock.RunAtMostEvery(ctx, cache.GetCache(), buildTask, buildEvery, 2*buildEvery, b.BuildAll)
}
