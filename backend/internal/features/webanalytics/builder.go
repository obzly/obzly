package webanalytics

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"
	logs_core "obzly/internal/features/logs/core"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/storage/segments"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
)

const (
	buildEvery = 5 * time.Minute
	// backfillWindow: how far back a project's missing hours are built,
	// e.g. on the first day it uses Web analytics.
	backfillWindow = 7 * 24 * time.Hour
	// hoursPerPass / passTimeBudget bound one project's work per pass, so a
	// backfill doesn't hold the worker.
	hoursPerPass   = 12
	passTimeBudget = 3 * time.Minute
	// hourScanTimeout bounds one slice's statements.
	hourScanTimeout = 2 * time.Minute
	// rowsPerSlice: access lines one slice of an hour is sized for, from
	// the busiest hour built lately (a slice past the caps is split anyway).
	rowsPerSlice = 1_000_000
	// maxHourFailures: hours that may fail in one project's pass before the
	// rest wait for the next pass.
	maxHourFailures = 2
	// keepHourly: hourly rows kept once their day is built (the 24 h and
	// 7 d views read hours; longer ranges read days).
	keepHourly = 9 * 24 * time.Hour
	// maxRetentionDays applies when the logs are kept forever.
	maxRetentionDays = 400
	// settleTime: an hour is built this long after it closes (plus the L0
	// buffer age with JetStream), so rows still being written are in it.
	settleTime = 4 * time.Minute
	// hourScanShare/Floor/Cap size an hour scan's DuckDB handle. A worker's
	// search cap (1/16 of the pod, 256 MB at 4 GiB) ran out of memory on
	// 5-minute slices of a busy project with multi-KB messages; the builder
	// runs one scan at a time on one replica, so it can take more.
	hourScanShare = 3.0 / 16
	hourScanFloor = 512_000_000
	hourScanCap   = 1_000_000_000
	// hourScanThreads: each thread holds its own Parquet row-group buffers.
	hourScanThreads = 1
)

// Builder summarises each closed hour of the projects that use Web
// analytics into rollups, a few minutes after the hour ends, then each
// closed day from its hours; on one worker replica (leader-locked).
type Builder struct {
	repo *repository
	logs func() logs_core.LogRepository
	geo  *countryDB
	log  *slog.Logger
	now  func() time.Time
}

// sourceIdentity is what Source.Identity stores.
type sourceIdentity struct {
	Attributes [][2]string `json:"attributes"`
	Name       string      `json:"name"`
	Service    string      `json:"service,omitempty"`
	HostName   string      `json:"hostName,omitempty"`
	Cluster    string      `json:"cluster,omitempty"`
	Namespace  string      `json:"namespace,omitempty"`
}

// Start runs the build loop until ctx ends.
func (b *Builder) Start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if _, err := leaderlock.RunExclusive(ctx, cache.GetCache(), "web-analytics-rollups", 2*buildEvery,
				b.BuildAll); err != nil {
				b.log.Error("web analytics rollups failed", "error", err)
			}
			timer.Reset(buildEvery)
		}
	}()
}

// BuildAll builds every active project's missing hours and days, one
// project at a time (each scan is a DuckDB handle under the worker's
// query memory cap), and applies retention.
func (b *Builder) BuildAll(ctx context.Context) error {
	projects, err := b.repo.activeProjects(ctx)
	if err != nil {
		return err
	}
	for i := range projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := b.BuildProject(ctx, &projects[i]); err != nil {
			b.log.Warn("web analytics rollups failed for project", "projectId", projects[i].ProjectID, "error", err)
		}
	}
	return nil
}

// BuildProject builds up to hoursPerPass of the project's missing closed
// hours, newest first, then its closed days, then drops expired rows.
func (b *Builder) BuildProject(ctx context.Context, settings *Settings) error {
	now := b.now()
	newest := lastBuildableHour(now)
	horizon := now.Truncate(time.Hour).Add(-backfillWindow)
	built, err := b.repo.builds(ctx, settings.ProjectID, granularityHour, horizon, newest.Add(time.Hour))
	if err != nil {
		return err
	}
	slice := sliceFor(built)
	started := time.Now()
	done, failed := 0, 0
	for hour := newest; !hour.Before(horizon); hour = hour.Add(-time.Hour) {
		if done >= hoursPerPass || failed >= maxHourFailures || time.Since(started) > passTimeBudget ||
			ctx.Err() != nil {
			break
		}
		if _, ok := built[hour]; ok {
			continue
		}
		done++
		hourStarted := time.Now()
		if err := b.buildHour(ctx, settings, hour, slice); err != nil {
			// Logged here, with what the scan got through; older hours still build.
			failed++
			attrs := []any{"projectId", settings.ProjectID, "hour", hour.Format(time.RFC3339),
				"slice", slice.String(), "durationMs", time.Since(hourStarted).Milliseconds(), "error", err}
			var scanErr *scanError
			if errors.As(err, &scanErr) {
				attrs = append(attrs, "rowsRead", scanErr.rowsRead, "slicesDone", scanErr.slices,
					"failedFrom", scanErr.from.Format(time.RFC3339), "failedTo", scanErr.to.Format(time.RFC3339))
			}
			b.log.Warn("web analytics hour failed", attrs...)
		}
	}
	if err := b.buildDays(ctx, settings, horizon, newest); err != nil {
		return err
	}
	return b.applyRetention(ctx, settings, now)
}

// BuildHour summarises one closed hour of raw logs and stores it. An hour
// without access logs is stored too (no rows), so reports read it as built.
func (b *Builder) BuildHour(ctx context.Context, settings *Settings, hour time.Time) error {
	return b.buildHour(ctx, settings, hour, 0)
}

// buildHour is BuildHour reading the hour in slices of slice (0: whole).
func (b *Builder) buildHour(ctx context.Context, settings *Settings, hour time.Time, slice time.Duration) error {
	started := time.Now()
	result, err := runScan(b.logs(), scanRequest{
		projectID: settings.ProjectID, from: hour, to: hour.Add(time.Hour), keysFrom: hour, bucket: time.Hour,
		options: optionsOf(settings), timeout: hourScanTimeout, slice: slice,
		memoryLimit: hourScanMemory(), threads: hourScanThreads,
	})
	if err != nil {
		return err
	}
	result.finish(b.geoFor(settings), settings.ExcludeBots)
	totals, keys, sources := rowsOf(settings.ProjectID, granularityHour, hour, result)
	if err := b.repo.upsertSources(ctx, sources); err != nil {
		return err
	}
	return b.repo.saveBucket(ctx, Build{
		ProjectID: settings.ProjectID, Granularity: granularityHour, BucketStart: hour, BuiltAt: b.now(),
		DurationMs: time.Since(started).Milliseconds(), RowsRead: result.rowsRead,
	}, totals, keys)
}

// buildDays merges each closed day whose hours (inside the backfill
// window) are all built into a day rollup, which ranges past a few days read.
func (b *Builder) buildDays(ctx context.Context, settings *Settings, horizon, newestHour time.Time) error {
	firstDay := horizon.Truncate(24 * time.Hour).Add(-48 * time.Hour)
	lastDay := newestHour.Add(time.Hour).Truncate(24 * time.Hour) // exclusive: its hours aren't all closed
	days, err := b.repo.builds(ctx, settings.ProjectID, granularityDay, firstDay, lastDay)
	if err != nil {
		return err
	}
	hours, err := b.repo.builds(ctx, settings.ProjectID, granularityHour, firstDay, lastDay)
	if err != nil {
		return err
	}
	for day := firstDay; day.Before(lastDay); day = day.Add(24 * time.Hour) {
		if _, ok := days[day]; ok {
			continue
		}
		var dayHours []time.Time
		complete := true
		for hour := day; hour.Before(day.Add(24 * time.Hour)); hour = hour.Add(time.Hour) {
			if _, ok := hours[hour]; ok {
				dayHours = append(dayHours, hour)
			} else if !hour.Before(horizon) {
				complete = false
			}
		}
		if !complete || len(dayHours) == 0 {
			continue
		}
		if err := b.buildDay(ctx, settings.ProjectID, day, dayHours); err != nil {
			return err
		}
	}
	return nil
}

func (b *Builder) buildDay(ctx context.Context, projectID uuid.UUID, day time.Time, hours []time.Time) error {
	started := time.Now()
	totalRows, err := b.repo.totalsOfBuckets(ctx, projectID, granularityHour, hours)
	if err != nil {
		return err
	}
	keyRows, err := b.repo.keysOfBuckets(ctx, projectID, granularityHour, hours)
	if err != nil {
		return err
	}
	merged := newRollup()
	for _, row := range totalRows {
		key := bucketKey{day, row.SourceKey, row.Host}
		if existing := merged.totals[key]; existing != nil {
			existing.merge(row.aggregate())
		} else {
			merged.totals[key] = row.aggregate()
		}
		merged.rowsRead += row.Requests
	}
	for _, row := range keyRows {
		sh := srcHost{row.SourceKey, row.Host}
		set := merged.keys[sh]
		if set == nil {
			set = keySet{}
			merged.keys[sh] = set
		}
		set.add(int(row.Dim), row.Key, row.aggregate())
	}
	for _, set := range merged.keys {
		set.truncate(func(int) int { return storedDayKeys })
	}
	var totals []TotalsRow
	for key, agg := range merged.totals {
		totals = append(totals, totalsRowOf(projectID, granularityDay, key, agg))
	}
	var keys []KeyRow
	for sh, set := range merged.keys {
		for dim, list := range set {
			for key, agg := range list {
				keys = append(keys, keyRowOf(projectID, granularityDay, day, sh, dim, key, agg))
			}
		}
	}
	return b.repo.saveBucket(ctx, Build{
		ProjectID: projectID, Granularity: granularityDay, BucketStart: day, BuiltAt: b.now(),
		DurationMs: time.Since(started).Milliseconds(), RowsRead: merged.rowsRead,
	}, totals, keys)
}

func (b *Builder) applyRetention(ctx context.Context, settings *Settings, now time.Time) error {
	days := rollupRetentionDays(settings)
	if err := b.repo.deleteBefore(ctx, settings.ProjectID, now.Add(-time.Duration(days)*24*time.Hour)); err != nil {
		return err
	}
	return b.repo.deleteHoursCoveredBy(ctx, settings.ProjectID, now.Add(-keepHourly))
}

func (b *Builder) geoFor(settings *Settings) *countryDB {
	if !settings.GeoEnabled {
		return nil
	}
	return b.geo
}

// rowsOf is a finished hourly scan's stored rows and the sources it saw.
// Sources are stored by the hash of their identity key.
func rowsOf(projectID uuid.UUID, granularity string, bucket time.Time, result *rollup,
) ([]TotalsRow, []KeyRow, []Source) {
	var totals []TotalsRow
	seen := map[string]*Source{}
	for key, agg := range result.totals {
		stored := key
		stored.src = sourceKey(key.src)
		totals = append(totals, totalsRowOf(projectID, granularity, stored, agg))
		source := seen[stored.src]
		if source == nil {
			source = sourceOf(projectID, key.src, agg, bucket)
			seen[stored.src] = source
		} else if agg.requests > 0 && source.Format == "" {
			source.Format = agg.format
		}
	}
	var keys []KeyRow
	for sh, set := range result.keys {
		stored := srcHost{sourceKey(sh.src), sh.host}
		for dim, list := range set {
			for key, agg := range list {
				keys = append(keys, keyRowOf(projectID, granularity, bucket, stored, dim, key, agg))
			}
		}
	}
	sources := make([]Source, 0, len(seen))
	for _, source := range seen {
		sources = append(sources, *source)
	}
	return totals, keys, sources
}

func sourceOf(projectID uuid.UUID, identity string, agg *totalsAgg, seenAt time.Time) *Source {
	by, attributes, name := identityParts(identity)
	data, _ := json.Marshal(sourceIdentity{
		Attributes: attributes, Name: name, Service: agg.service, HostName: agg.hostName, Cluster: agg.cluster,
		Namespace: agg.namespace,
	})
	return &Source{
		ProjectID: projectID, SourceKey: sourceKey(identity), IdentifiedBy: by, Identity: data, Format: agg.format,
		FirstSeen: seenAt, LastSeen: seenAt,
	}
}

// sliceFor sizes an hour's slices for about rowsPerSlice access lines
// each, from the busiest of the recently built hours (0: the whole hour).
func sliceFor(built map[time.Time]Build) time.Duration {
	var busiest int64
	for _, build := range built {
		busiest = max(busiest, build.RowsRead)
	}
	if busiest <= rowsPerSlice {
		return 0
	}
	slices := (busiest + rowsPerSlice - 1) / rowsPerSlice
	return max((time.Hour / time.Duration(slices)).Truncate(time.Minute), minScanSlice)
}

func optionsOf(settings *Settings) scanOptions {
	return scanOptions{
		ipMode: settings.IPMode, salt: hexSalt(settings.HashSalt), excludeBots: settings.ExcludeBots,
		staticExtensions: sanitizeStaticExtensions(settings.StaticExtensions),
	}
}

// lastBuildableHour is the newest hour that closed at least buildLag ago.
func lastBuildableHour(now time.Time) time.Time {
	return now.Add(-buildLag()).Truncate(time.Hour).Add(-time.Hour)
}

// buildLag: how long after an hour closes its rows are all queryable.
// With JetStream a low-volume project's rows wait up to INGEST_L0_MAX_AGE
// before they reach a segment the worker can read.
func buildLag() time.Duration {
	env := config.GetEnv()
	if !env.UsesJetStream() {
		return settleTime
	}
	age := env.IngestL0MaxAge
	if age <= 0 {
		age = config.DefaultIngestL0MaxAge
	}
	return age + settleTime
}

// rollupRetentionDays: the setting, else the project's log retention,
// else maxRetentionDays.
func rollupRetentionDays(settings *Settings) int {
	if settings.RetentionDays > 0 {
		return settings.RetentionDays
	}
	if days := logRetentionDays(settings.ProjectID); days > 0 {
		return min(days, maxRetentionDays)
	}
	return maxRetentionDays
}

// logRetentionDays is how long the project keeps logs (0: forever or unknown).
func logRetentionDays(projectID uuid.UUID) int {
	project, err := projects_services.GetProjectService().GetProjectWithCache(projectID)
	if err != nil || project == nil {
		return 0
	}
	policies, err := projects_services.GetRetentionPolicyService().PoliciesFor(projectID)
	if err != nil {
		return 0
	}
	return project.EffectiveRetentionDays(policies[projects_models.SignalLogs])
}

func hexSalt(salt []byte) string {
	return hex.EncodeToString(salt)
}

// hourScanMemory is an hour scan's DuckDB memory limit in bytes.
func hourScanMemory() int64 {
	return segments.MemoryLimitBytes(
		segments.ContainerShare(segments.ContainerMemoryLimit(), hourScanShare, hourScanFloor, hourScanCap))
}
