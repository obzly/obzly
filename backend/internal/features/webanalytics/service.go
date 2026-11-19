package webanalytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"obzly/internal/authz"
	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

const (
	// shortRange: ranges under this are read entirely from raw logs.
	shortRange = 2 * time.Hour
	maxRange   = 400 * 24 * time.Hour
	// maxLiveClosedHours: closed hours not yet built that a report reads
	// from raw logs (beyond that they show as "not summarised yet").
	maxLiveClosedHours = 3
	// panelRows: rows each top list returns.
	panelRows = 50
	// previousCoverage: the share of the previous period's hours that must
	// be summarised for change pills; otherwise they are n/a.
	previousCoverage = 0.9
	// sourceListWindow: sources not seen for this long leave the picker.
	sourceListWindow = 30 * 24 * time.Hour
	liveScanTimeout  = time.Minute
	// liveScanBudget bounds a report's whole raw read (every slice, under
	// the UI's 30 s request timeout); what doesn't fit is left out and the
	// report says its live part is partial.
	liveScanBudget = 20 * time.Second
	// liveScanSlice: a live read goes an hour at a time, newest first, so
	// the current hour comes first when the budget runs out.
	liveScanSlice   = time.Hour
	dbipAttribution = "IP geolocation by DB-IP"
)

var (
	ErrForbidden       = errors.New("you can't read this project")
	ErrManageForbidden = errors.New("only project admins can change Web analytics settings")
	ErrInvalidRange    = errors.New("invalid time range: from must be before to, at most 400 days")
)

// ValidationError is a settings change the service refuses.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

type auditWriter interface {
	WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID)
}

// Service answers the Web analytics page.
type Service struct {
	repo  *repository
	logs  func() logs_core.LogRepository
	geo   *countryDB
	audit auditWriter
	now   func() time.Time
	live  *liveCache
}

// ReportParams: zero From/To mean the last 24 hours; empty Sources all.
type ReportParams struct {
	From    time.Time
	To      time.Time
	Sources []string
	Host    string
}

// rangePlan is which part of a range comes from where.
type rangePlan struct {
	short          bool
	bucket         time.Duration
	from, to       time.Time
	liveFrom       time.Time
	liveBucket     time.Duration
	keysFrom       time.Time
	totals         bucketSelection
	keys           bucketSelection
	previous       bucketSelection
	previousFrom   time.Time
	hasPrevious    bool
	rollupHours    int
	rollupFrom     *time.Time
	rollupTo       *time.Time
	lastBuiltAt    *time.Time
	missing        int
	previousLength time.Duration
}

// sourceView resolves merges: canonical is what a raw source counts as.
type sourceView struct {
	byKey     map[string]*Source
	canonical map[string]string
	children  map[string][]string
}

// Report builds the Web analytics report for a range. The first report of
// a project creates its settings, which starts its hourly rollups.
func (s *Service) Report(ctx context.Context, user *users_models.User, projectID uuid.UUID, params ReportParams,
) (*ReportDTO, error) {
	if err := s.authorize(user, projectID, authz.ProjectRead, ErrForbidden); err != nil {
		return nil, err
	}
	settings, err := s.repo.ensureSettings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	plan, err := s.plan(ctx, projectID, params, now)
	if err != nil {
		return nil, err
	}

	liveResult, liveInfo, err := s.liveScan(ctx, settings, plan, now)
	if err != nil {
		return nil, err
	}
	if err := s.repo.upsertSources(ctx, liveSources(projectID, liveResult)); err != nil {
		return nil, err
	}
	live := rekey(liveResult)

	storedSources, err := s.repo.sources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	view := newSourceView(storedSources)
	totalRows, err := s.repo.totals(ctx, projectID, plan.totals, keyFilter{})
	if err != nil {
		return nil, err
	}

	selected, rawSelected := view.selection(params.Sources)
	filter := keyFilter{sources: rawSelected, host: params.Host}
	inFilter := func(src, host string) bool {
		return contains(rawSelected, src) && (params.Host == "" || params.Host == host)
	}

	report := &ReportDTO{
		From: plan.from, To: plan.to, BucketSeconds: int64(plan.bucket / time.Second), Live: liveInfo,
		Selected: selected, IPMode: settings.IPMode, ExcludeBots: settings.ExcludeBots,
		Rollups: RollupInfoDTO{Used: !plan.short, Hours: plan.rollupHours, From: plan.rollupFrom, To: plan.rollupTo,
			LastBuiltAt: plan.lastBuiltAt, Missing: plan.missing},
	}

	perSource := map[string]*totalsAgg{}
	hosts := map[string]map[string]int64{}
	overall := newTotals()
	series := map[time.Time]*totalsAgg{}
	previous := newTotals()
	addTotals := func(src, host string, bucket time.Time, agg *totalsAgg) {
		canonical := view.canonicalOf(src)
		if bucket.Before(plan.from) {
			if plan.short {
				if inFilter(src, host) {
					previous.merge(agg)
				}
				return
			}
			// An old day whose hours are gone starts before the range.
			bucket = plan.from
		}
		mergeInto(perSource, canonical, agg)
		if hosts[canonical] == nil {
			hosts[canonical] = map[string]int64{}
		}
		hosts[canonical][host] += agg.requests
		if inFilter(src, host) {
			overall.merge(agg)
			mergeInto(series, bucket.Truncate(plan.bucket), agg)
		}
	}
	for _, row := range totalRows {
		addTotals(row.SourceKey, row.Host, row.BucketStart.UTC(), row.aggregate())
	}
	for key, agg := range live.totals {
		addTotals(key.src, key.host, key.bucket, agg)
	}

	report.Sources = s.sourceDTOs(view, perSource, hosts, now)
	report.Hosts = hostList(hosts, selected, view)
	report.Totals = totalsDTO(overall, view, rawSelected, perSource)
	report.Series = seriesDTO(series, plan, now)

	if plan.hasPrevious {
		previousRows, err := s.repo.totals(ctx, projectID, plan.previous, filter)
		if err != nil {
			return nil, err
		}
		for _, row := range previousRows {
			previous.merge(row.aggregate())
		}
		dto := totalsDTO(previous, view, rawSelected, perSource)
		report.Previous = &dto
	} else if plan.short {
		dto := totalsDTO(previous, view, rawSelected, perSource)
		report.Previous = &dto
	}

	report.Heatmap, err = s.heatmap(ctx, projectID, filter, live, inFilter, now)
	if err != nil {
		return nil, err
	}
	report.Panels, err = s.panels(ctx, projectID, plan, filter, live, inFilter, overall, totalRows, settings)
	if err != nil {
		return nil, err
	}
	report.Geo = s.geoDTO(settings)
	return report, nil
}

// Settings is the settings drawer's content.
func (s *Service) Settings(ctx context.Context, user *users_models.User, projectID uuid.UUID) (*SettingsDTO, error) {
	if err := s.authorize(user, projectID, authz.ProjectRead, ErrForbidden); err != nil {
		return nil, err
	}
	settings, err := s.repo.ensureSettings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sources, err := s.repo.sources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	last, err := s.repo.lastBuild(ctx, projectID)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.storage(ctx, projectID)
	if err != nil {
		return nil, err
	}
	decision, _ := authz.Authorize(ctx, user, authz.ProjectManage, authz.Project(projectID))
	dto := &SettingsDTO{
		IPMode: settings.IPMode, RetentionDays: settings.RetentionDays, LogRetentionDays: logRetentionDays(projectID),
		ExcludeBots: settings.ExcludeBots, StaticExtensions: settings.StaticExtensions, GeoEnabled: settings.GeoEnabled,
		Geo: s.geoDTO(settings), Sources: s.sourceDTOs(newSourceView(sources), nil, nil, s.now()),
		Storage: StorageDTO(stats), Schema: schemaDTO(), CanManage: decision.Allowed,
		BuildDelayMin: int(buildLag() / time.Minute),
	}
	if last != nil {
		dto.Build = &BuildDTO{Hour: last.BucketStart.UTC(), BuiltAt: last.BuiltAt.UTC(), DurationMs: last.DurationMs,
			RowsRead: last.RowsRead}
	}
	return dto, nil
}

// UpdateSettings changes the settings and source names; privacy settings
// apply to hours built afterwards.
func (s *Service) UpdateSettings(ctx context.Context, user *users_models.User, projectID uuid.UUID,
	request *UpdateSettingsDTO) (*SettingsDTO, error) {
	if err := s.authorize(user, projectID, authz.ProjectManage, ErrManageForbidden); err != nil {
		return nil, err
	}
	settings, err := s.repo.ensureSettings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var changes []string
	if request.IPMode != nil && *request.IPMode != settings.IPMode {
		switch *request.IPMode {
		case ipModeTruncate, ipModeHash, ipModeFull:
		default:
			return nil, &ValidationError{"ipMode must be truncate, hash or full"}
		}
		changes = append(changes, fmt.Sprintf("visitor IPs %s → %s", settings.IPMode, *request.IPMode))
		settings.IPMode = *request.IPMode
	}
	if request.RetentionDays != nil && *request.RetentionDays != settings.RetentionDays {
		if *request.RetentionDays < 0 || *request.RetentionDays > 3650 {
			return nil, &ValidationError{"retentionDays must be between 0 (like the logs) and 3650"}
		}
		changes = append(changes, fmt.Sprintf("rollup retention %s → %s", retentionText(settings.RetentionDays),
			retentionText(*request.RetentionDays)))
		settings.RetentionDays = *request.RetentionDays
	}
	if request.ExcludeBots != nil && *request.ExcludeBots != settings.ExcludeBots {
		changes = append(changes, fmt.Sprintf("leave crawlers out %t → %t", settings.ExcludeBots, *request.ExcludeBots))
		settings.ExcludeBots = *request.ExcludeBots
	}
	if request.StaticExtensions != nil {
		extensions := staticExtensionsText(sanitizeStaticExtensions(*request.StaticExtensions))
		if extensions != settings.StaticExtensions {
			changes = append(changes, "static file extensions")
			settings.StaticExtensions = extensions
		}
	}
	if request.GeoEnabled != nil && *request.GeoEnabled != settings.GeoEnabled {
		changes = append(changes, fmt.Sprintf("country lookup %t → %t", settings.GeoEnabled, *request.GeoEnabled))
		settings.GeoEnabled = *request.GeoEnabled
	}
	sourceChanges, err := s.updateSources(ctx, projectID, request.Sources)
	if err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		settings.UpdatedAt = s.now()
		if err := s.repo.saveSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	for _, change := range append(changes, sourceChanges...) {
		s.audit.WriteAuditLog("Web analytics settings changed: "+change, &user.ID, &projectID)
	}
	return s.Settings(ctx, user, projectID)
}

func (s *Service) updateSources(ctx context.Context, projectID uuid.UUID, updates []UpdateSourceDTO) ([]string, error) {
	if len(updates) == 0 {
		return nil, nil
	}
	stored, err := s.repo.sources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Source{}
	for i := range stored {
		byKey[stored[i].SourceKey] = &stored[i]
	}
	var changed []Source
	var messages []string
	for _, update := range updates {
		source := byKey[update.Key]
		if source == nil {
			return nil, &ValidationError{"unknown source " + update.Key}
		}
		name := strings.TrimSpace(update.Name)
		if len(name) > 120 {
			return nil, &ValidationError{"a source name is at most 120 characters"}
		}
		label := displayName(source)
		if name != source.Name {
			if name == "" {
				messages = append(messages, fmt.Sprintf("source %q back to its default name", label))
			} else {
				messages = append(messages, fmt.Sprintf("source %q renamed to %q", label, name))
			}
			source.Name = name
		}
		current := ""
		if source.MergedInto != nil {
			current = *source.MergedInto
		}
		if update.MergedInto != current {
			if update.MergedInto == "" {
				source.MergedInto = nil
				messages = append(messages, fmt.Sprintf("source %q unmerged", label))
			} else {
				target := byKey[update.MergedInto]
				if target == nil || target.SourceKey == source.SourceKey {
					return nil, &ValidationError{"a source can only be merged into another known source"}
				}
				into := target.SourceKey
				source.MergedInto = &into
				messages = append(messages, fmt.Sprintf("source %q merged into %q", label, displayName(target)))
			}
		}
		changed = append(changed, *source)
	}
	// One level only: a source merged into one that is itself merged
	// would hide where its requests went.
	for _, source := range byKey {
		if source.MergedInto == nil {
			continue
		}
		if target := byKey[*source.MergedInto]; target == nil || target.MergedInto != nil {
			return nil, &ValidationError{"merge into a source that isn't merged itself"}
		}
	}
	if err := s.repo.saveSourceNames(ctx, projectID, changed); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Service) authorize(user *users_models.User, projectID uuid.UUID, action authz.Action, denied error) error {
	decision, err := authz.Authorize(context.Background(), user, action, authz.Project(projectID))
	if err != nil || !decision.Allowed {
		return denied
	}
	return nil
}

// plan decides which buckets come from rollups, which hours are read live,
// and the previous period for change pills.
func (s *Service) plan(ctx context.Context, projectID uuid.UUID, params ReportParams, now time.Time,
) (*rangePlan, error) {
	from, to := params.From.UTC(), params.To.UTC()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) || to.Sub(from) > maxRange {
		return nil, ErrInvalidRange
	}
	length := to.Sub(from)
	if length < shortRange {
		bucket := 5 * time.Minute
		if length <= 20*time.Minute {
			bucket = time.Minute
		}
		from = from.Truncate(bucket)
		length = to.Sub(from)
		return &rangePlan{short: true, bucket: bucket, from: from, to: to, liveFrom: from.Add(-length),
			liveBucket: bucket, keysFrom: from, previousFrom: from.Add(-length), previousLength: length}, nil
	}

	plan := &rangePlan{bucket: seriesBucket(length), to: to, liveBucket: time.Hour}
	liveStart := now.Truncate(time.Hour)
	rollupFrom := ceilHour(from)
	if rollupFrom.After(liveStart) {
		rollupFrom = liveStart
	}
	plan.from = rollupFrom
	span := to.Sub(rollupFrom)
	previousFrom := rollupFrom.Add(-span).Truncate(time.Hour)
	hourBuilds, err := s.repo.builds(ctx, projectID, granularityHour, previousFrom, liveStart)
	if err != nil {
		return nil, err
	}
	dayBuilds, err := s.repo.builds(ctx, projectID, granularityDay, previousFrom.Truncate(24*time.Hour), liveStart)
	if err != nil {
		return nil, err
	}
	covered := func(hour time.Time) bool {
		_, hourBuilt := hourBuilds[hour]
		_, dayBuilt := dayBuilds[hour.Truncate(24*time.Hour)]
		return hourBuilt || dayBuilt
	}

	liveFrom := liveStart
	for k := 1; k <= maxLiveClosedHours; k++ {
		hour := liveStart.Add(-time.Duration(k) * time.Hour)
		if hour.Before(rollupFrom) || covered(hour) {
			break
		}
		liveFrom = hour
	}
	plan.liveFrom, plan.keysFrom = liveFrom, liveFrom

	plan.totals, plan.keys, plan.rollupHours, plan.missing = selectBuckets(rollupFrom, liveFrom, hourBuilds, dayBuilds,
		plan.bucket >= 24*time.Hour)
	for hour, build := range hourBuilds {
		if !hour.Before(rollupFrom) && hour.Before(liveFrom) &&
			(plan.lastBuiltAt == nil || build.BuiltAt.After(*plan.lastBuiltAt)) {
			builtAt := build.BuiltAt.UTC()
			plan.lastBuiltAt = &builtAt
		}
	}
	if plan.rollupHours > 0 {
		start, end := rollupFrom, liveFrom
		plan.rollupFrom, plan.rollupTo = &start, &end
	}

	previous, _, previousHours, previousMissing := selectBuckets(previousFrom, rollupFrom, hourBuilds, dayBuilds, true)
	if total := previousHours + previousMissing; total > 0 &&
		float64(previousHours)/float64(total) >= previousCoverage {
		plan.previous, plan.hasPrevious = previous, true
	}
	plan.previousFrom, plan.previousLength = previousFrom, span
	return plan, nil
}

// selectBuckets picks the stored buckets covering [from, to): whole UTC
// days where a day is built (for top lists always, for counts only when
// wholeDays), single hours otherwise. It counts the hours covered and
// those not summarised yet.
func selectBuckets(from, to time.Time, hours, days map[time.Time]Build, wholeDays bool,
) (totals, keys bucketSelection, covered, missing int) {
	usedDays := map[time.Time]bool{}
	for hour := from; hour.Before(to); hour = hour.Add(time.Hour) {
		day := hour.Truncate(24 * time.Hour)
		_, dayBuilt := days[day]
		_, hourBuilt := hours[hour]
		dayInside := !day.Before(from) && !day.Add(24*time.Hour).After(to)
		switch {
		case dayBuilt && dayInside:
			if !usedDays[day] {
				usedDays[day] = true
				keys.days = append(keys.days, day)
				if wholeDays || !hourBuilt {
					totals.days = append(totals.days, day)
				}
			}
			if !wholeDays && hourBuilt {
				totals.hours = append(totals.hours, hour)
			}
			covered++
		case hourBuilt:
			keys.hours = append(keys.hours, hour)
			totals.hours = append(totals.hours, hour)
			covered++
		case dayBuilt:
			// Hourly rows of an old day are gone; the day is the best there is.
			if !usedDays[day] {
				usedDays[day] = true
				keys.days = append(keys.days, day)
				totals.days = append(totals.days, day)
			}
			covered++
		default:
			missing++
		}
	}
	return totals, keys, covered, missing
}

// liveScan reads the plan's live part from raw logs (cached 30 s, so Live
// mode and several viewers share one read), within liveScanBudget: a slice
// that fails or doesn't fit is left out and the live part marked partial,
// so the report still has the rollups.
func (s *Service) liveScan(ctx context.Context, settings *Settings, plan *rangePlan, now time.Time,
) (*rollup, LiveInfoDTO, error) {
	info := LiveInfoDTO{From: plan.liveFrom, To: now}
	if !plan.liveFrom.Before(now) {
		return newRollup(), info, nil
	}
	request := scanRequest{
		projectID: settings.ProjectID, from: plan.liveFrom, to: now, keysFrom: plan.keysFrom, bucket: plan.liveBucket,
		options: optionsOf(settings), timeout: liveScanTimeout, slice: liveScanSlice, partial: true,
	}
	key := fmt.Sprintf("%s|%d|%d|%d|%d|%d", settings.ProjectID, plan.liveFrom.Unix(), now.Unix()/30,
		int64(plan.liveBucket), plan.keysFrom.Unix(), settings.UpdatedAt.UnixNano())
	entry, cached, err := s.live.get(ctx, key, func() (*rollup, error) {
		started := time.Now()
		request.deadline = started.Add(liveScanBudget)
		result, err := runScan(s.logs(), request)
		if result == nil {
			return nil, err
		}
		if err != nil {
			logger.GetLogger().Warn("web analytics live read is partial", "projectId", settings.ProjectID,
				"from", request.from.Format(time.RFC3339), "to", request.to.Format(time.RFC3339),
				"rowsRead", result.rowsRead, "durationMs", time.Since(started).Milliseconds(), "error", err)
		}
		geo := s.geo
		if !settings.GeoEnabled {
			geo = nil
		}
		result.finish(geo, settings.ExcludeBots)
		return result, nil
	})
	if err != nil {
		return nil, info, err
	}
	info.To, info.Rows, info.DurationMs, info.Cached = entry.to, entry.result.rowsRead, entry.durationMs, cached
	info.Partial = entry.result.partial
	return entry.result, info, nil
}

func (s *Service) heatmap(ctx context.Context, projectID uuid.UUID, filter keyFilter, live *rollup,
	inFilter func(src, host string) bool, now time.Time) ([]HourCountDTO, error) {
	end := now.Truncate(time.Hour).Add(time.Hour)
	start := end.Add(-7 * 24 * time.Hour)
	counts, err := s.repo.hourlyRequests(ctx, projectID, start, end, filter)
	if err != nil {
		return nil, err
	}
	// Live hours that are also stored (a short range reads closed hours
	// raw too) count once, from the rollup.
	stored := make(map[time.Time]bool, len(counts))
	for hour := range counts {
		stored[hour] = true
	}
	for key, agg := range live.totals {
		hour := key.bucket.Truncate(time.Hour)
		if inFilter(key.src, key.host) && !hour.Before(start) && !stored[hour] {
			counts[hour] += agg.requests
		}
	}
	result := make([]HourCountDTO, 0, len(counts))
	for hour, requests := range counts {
		result = append(result, HourCountDTO{Hour: hour, Requests: requests})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Hour.Before(result[j].Hour) })
	return result, nil
}

// panels merges the stored top lists of the selection with the live part.
func (s *Service) panels(ctx context.Context, projectID uuid.UUID, plan *rangePlan, filter keyFilter, live *rollup,
	inFilter func(src, host string) bool, overall *totalsAgg, totalRows []TotalsRow, settings *Settings,
) (PanelsDTO, error) {
	merged := keySet{}
	ranked, err := s.repo.rankedKeys(ctx, projectID, plan.keys, filter, panelRows*2)
	if err != nil {
		return PanelsDTO{}, err
	}
	var sketchDimList []int16
	var sketchKeys []string
	for _, row := range ranked {
		agg := &keyAgg{hits: row.Hits, bytes: row.Bytes, e5: row.Errors5xx, referred: row.Referred, extra: row.Extra}
		if row.LastSeen != nil {
			agg.lastSeen = row.LastSeen.UTC()
		}
		if sketchDims[int(row.Dim)] {
			agg.visitors = newSketch(keyPrecision)
			sketchDimList = append(sketchDimList, row.Dim)
			sketchKeys = append(sketchKeys, row.Key)
		}
		merged.add(int(row.Dim), row.Key, agg)
	}
	sketches, err := s.repo.keySketches(ctx, projectID, plan.keys, filter, sketchDimList, sketchKeys)
	if err != nil {
		return PanelsDTO{}, err
	}
	for _, row := range sketches {
		if existing := merged[int(row.Dim)][row.Key]; existing != nil && existing.visitors != nil {
			if visitors, err := decodeSketch(row.VisitorsHLL, keyPrecision); err == nil {
				existing.visitors.merge(visitors)
			}
		}
	}
	for sh, set := range live.keys {
		if !inFilter(sh.src, sh.host) {
			continue
		}
		for dim, list := range set {
			for key, agg := range list {
				merged.add(dim, key, agg)
			}
		}
	}

	visitorsKnown := overall.with[withClient] > 0
	rows := func(dim int) []RowDTO {
		list := merged[dim]
		result := make([]RowDTO, 0, min(len(list), panelRows))
		for _, key := range topKeys(list, panelRows) {
			agg := list[key]
			row := RowDTO{Key: key, Hits: agg.hits, Bytes: agg.bytes, Errors5xx: agg.e5}
			if agg.visitors != nil && visitorsKnown {
				visitors := agg.visitors.estimate()
				row.Visitors = &visitors
			}
			switch dim {
			case dimNotFound:
				lastSeen := agg.lastSeen
				if !lastSeen.IsZero() {
					row.LastSeen = &lastSeen
				}
				row.Tag = notFoundTag(key, agg.referred)
			case dimReferrer:
				row.Tag = referrerTag(key)
			case dimIP:
				row.Country = agg.extra
			}
			result = append(result, row)
		}
		return result
	}
	panels := PanelsDTO{
		Pages: rows(dimPage), NotFound: rows(dimNotFound), Static: rows(dimStatic), Referrers: rows(dimReferrer),
		IPs: rows(dimIP), Browsers: rows(dimBrowser), Systems: rows(dimOS), Devices: rows(dimDevice),
		Countries: rows(dimCountry), Methods: rows(dimMethod), Protocols: rows(dimProtocol), TLS: rows(dimTLS),
		Status: rows(dimStatus),
	}
	if !settings.GeoEnabled {
		panels.Countries = []RowDTO{}
	}
	panels.VHosts = vhostRows(totalRows, live, plan, inFilter)
	return panels, nil
}

func (s *Service) geoDTO(settings *Settings) GeoDTO {
	info := s.geo.Info()
	return GeoDTO{Enabled: settings.GeoEnabled, Available: info.Available, DatabaseType: info.DatabaseType,
		Attribution: dbipAttribution, Error: info.Error}
}

// sourceDTOs lists the canonical sources: those seen within
// sourceListWindow or with requests in the range, busiest first.
func (s *Service) sourceDTOs(view *sourceView, perSource map[string]*totalsAgg, hosts map[string]map[string]int64,
	now time.Time) []SourceDTO {
	var total int64
	for _, agg := range perSource {
		total += agg.requests
	}
	result := []SourceDTO{}
	for key, source := range view.byKey {
		if view.canonical[key] != key {
			continue
		}
		stats := perSource[key]
		lastSeen := source.LastSeen
		for _, child := range view.children[key] {
			if seen := view.byKey[child].LastSeen; seen.After(lastSeen) {
				lastSeen = seen
			}
		}
		if stats == nil && perSource != nil && now.Sub(lastSeen) > sourceListWindow {
			continue
		}
		dto := sourceDTO(source, view)
		dto.LastSeen = lastSeen
		if stats != nil {
			dto.Requests = stats.requests
			if total > 0 {
				dto.Share = float64(stats.requests) / float64(total)
			}
			dto.HasReferrer = stats.with[withReferer] > 0
			dto.HasUserAgent = stats.with[withUA] > 0
			dto.HasDuration = stats.with[withDuration] > 0
			dto.HasTLS = stats.with[withTLS] > 0
			if stats.format != "" {
				format := decodeFormat(stats.format)
				dto.Format, dto.Fields = format.Dialect, format.Fields
				dto.Kind = kindOf(source, format.Dialect)
			}
		}
		dto.Hosts = topHosts(hosts[key], 10)
		result = append(result, dto)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests != result[j].Requests {
			return result[i].Requests > result[j].Requests
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func sourceDTO(source *Source, view *sourceView) SourceDTO {
	var identity sourceIdentity
	_ = json.Unmarshal(source.Identity, &identity)
	format := decodeFormat(source.Format)
	attributes := make([]AttributeDTO, 0, len(identity.Attributes))
	for _, pair := range identity.Attributes {
		attributes = append(attributes, AttributeDTO{Name: pair[0], Value: pair[1]})
	}
	dto := SourceDTO{
		Key: source.SourceKey, Name: displayName(source), DefaultName: identity.Name, IdentifiedBy: source.IdentifiedBy,
		Identity: attributes, Kind: kindOf(source, format.Dialect), Format: format.Dialect, Fields: format.Fields,
		Where: whereOf(identity), Hosts: []string{}, MergedFrom: append([]string{}, view.children[source.SourceKey]...),
		FirstSeen: source.FirstSeen.UTC(), LastSeen: source.LastSeen.UTC(),
		HasReferrer: format.Fields[fieldReferer] != "", HasUserAgent: format.Fields[fieldUA] != "",
		HasDuration: format.Fields[fieldDuration] != "", HasTLS: format.Fields[fieldTLS] != "",
	}
	if source.MergedInto != nil {
		dto.MergedInto = *source.MergedInto
	}
	return dto
}

func newSourceView(sources []Source) *sourceView {
	view := &sourceView{byKey: map[string]*Source{}, canonical: map[string]string{}, children: map[string][]string{}}
	for i := range sources {
		view.byKey[sources[i].SourceKey] = &sources[i]
	}
	for key, source := range view.byKey {
		view.canonical[key] = key
		if source.MergedInto != nil {
			if target := view.byKey[*source.MergedInto]; target != nil && target.MergedInto == nil {
				view.canonical[key] = target.SourceKey
				view.children[target.SourceKey] = append(view.children[target.SourceKey], key)
			}
		}
	}
	for _, children := range view.children {
		sort.Strings(children)
	}
	return view
}

func (v *sourceView) canonicalOf(key string) string {
	if canonical, ok := v.canonical[key]; ok {
		return canonical
	}
	return key
}

// selection resolves requested canonical keys (empty: all) to themselves
// and every raw key that counts as them.
func (v *sourceView) selection(requested []string) (selected, raw []string) {
	want := map[string]bool{}
	for _, key := range requested {
		if key = strings.TrimSpace(key); key != "" {
			want[v.canonicalOf(key)] = true
		}
	}
	raw = []string{}
	selected = []string{}
	for key := range v.byKey {
		canonical := v.canonical[key]
		if len(want) == 0 || want[canonical] {
			raw = append(raw, key)
			if canonical == key {
				selected = append(selected, key)
			}
		}
	}
	sort.Strings(selected)
	sort.Strings(raw)
	return selected, raw
}

// liveSources are the sources a live scan saw, to list new ones at once.
func liveSources(projectID uuid.UUID, result *rollup) []Source {
	seen := map[string]Source{}
	for key, agg := range result.totals {
		if _, ok := seen[key.src]; !ok {
			seen[key.src] = *sourceOf(projectID, key.src, agg, key.bucket)
		}
	}
	list := make([]Source, 0, len(seen))
	for _, source := range seen {
		list = append(list, source)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SourceKey < list[j].SourceKey })
	return list
}

// rekey is a scan's rollup keyed by stored source keys, as rollups are.
func rekey(result *rollup) *rollup {
	out := newRollup()
	out.rowsRead = result.rowsRead
	for key, agg := range result.totals {
		out.totals[bucketKey{key.bucket, sourceKey(key.src), key.host}] = agg
	}
	for sh, set := range result.keys {
		out.keys[srcHost{sourceKey(sh.src), sh.host}] = set
	}
	return out
}

func totalsDTO(agg *totalsAgg, view *sourceView, rawSelected []string, perSource map[string]*totalsAgg) TotalsDTO {
	dto := TotalsDTO{Requests: agg.requests, Failed: agg.s4 + agg.s5, NotFound: agg.notFound, Bytes: agg.bytes,
		BotRequests: agg.bots, DurationSources: []string{}}
	if agg.with[withClient] > 0 {
		visitors := agg.visitors.estimate()
		dto.Visitors = &visitors
	}
	if p95, ok := percentile(agg.hist, 0.95); ok {
		dto.P95Ms = &p95
	}
	seen := map[string]bool{}
	for _, key := range rawSelected {
		canonical := view.canonicalOf(key)
		if stats := perSource[canonical]; stats != nil && stats.with[withDuration] > 0 && !seen[canonical] {
			seen[canonical] = true
			dto.DurationSources = append(dto.DurationSources, canonical)
		}
	}
	sort.Strings(dto.DurationSources)
	return dto
}

func seriesDTO(series map[time.Time]*totalsAgg, plan *rangePlan, now time.Time) []BucketDTO {
	result := []BucketDTO{}
	for start := plan.from.Truncate(plan.bucket); start.Before(now); start = start.Add(plan.bucket) {
		agg := series[start]
		bucket := BucketDTO{Start: start, Live: !plan.short && start.Add(plan.bucket).After(plan.liveFrom)}
		if agg != nil {
			bucket.Requests, bucket.Status2, bucket.Status3 = agg.requests, agg.s2, agg.s3
			bucket.Status4, bucket.Status5, bucket.Other, bucket.Bytes = agg.s4, agg.s5, agg.sOther, agg.bytes
			bucket.Visitors = agg.visitors.estimate()
		}
		result = append(result, bucket)
	}
	return result
}

func vhostRows(totalRows []TotalsRow, live *rollup, plan *rangePlan, inFilter func(src, host string) bool) []RowDTO {
	byHost := map[string]*RowDTO{}
	add := func(host string, bucket time.Time, agg *totalsAgg) {
		if (plan.short && bucket.Before(plan.from)) || host == "" {
			return
		}
		row := byHost[host]
		if row == nil {
			row = &RowDTO{Key: host}
			byHost[host] = row
		}
		row.Hits += agg.requests
		row.Bytes += agg.bytes
		row.Errors5xx += agg.s5
	}
	for _, row := range totalRows {
		if inFilter(row.SourceKey, row.Host) {
			add(row.Host, row.BucketStart.UTC(), row.aggregate())
		}
	}
	for key, agg := range live.totals {
		if inFilter(key.src, key.host) {
			add(key.host, key.bucket, agg)
		}
	}
	result := make([]RowDTO, 0, len(byHost))
	for _, row := range byHost {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Hits != result[j].Hits {
			return result[i].Hits > result[j].Hits
		}
		return result[i].Key < result[j].Key
	})
	if len(result) > panelRows {
		result = result[:panelRows]
	}
	return result
}

// hostList is the Host picker: hosts of the selected sources, busiest first.
func hostList(hosts map[string]map[string]int64, selected []string, view *sourceView) []HostDTO {
	sum := map[string]int64{}
	for _, key := range selected {
		for host, requests := range hosts[view.canonicalOf(key)] {
			if host != "" {
				sum[host] += requests
			}
		}
	}
	result := make([]HostDTO, 0, len(sum))
	for host, requests := range sum {
		result = append(result, HostDTO{Host: host, Requests: requests})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests != result[j].Requests {
			return result[i].Requests > result[j].Requests
		}
		return result[i].Host < result[j].Host
	})
	return result
}

func topHosts(hosts map[string]int64, n int) []string {
	list := make([]string, 0, len(hosts))
	for host := range hosts {
		if host != "" {
			list = append(list, host)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if hosts[list[i]] != hosts[list[j]] {
			return hosts[list[i]] > hosts[list[j]]
		}
		return list[i] < list[j]
	})
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// percentile interpolates the q-th quantile inside its histogram bucket.
func percentile(hist []int64, q float64) (float64, bool) {
	var total int64
	for _, count := range hist {
		total += count
	}
	if total == 0 {
		return 0, false
	}
	target := q * float64(total)
	var cumulative float64
	for i, count := range hist {
		next := cumulative + float64(count)
		if next >= target && count > 0 {
			lower := 0.0
			if i > 0 {
				lower = durationBoundsMs[i-1]
			}
			upper := lower * 2
			if i < len(durationBoundsMs) {
				upper = durationBoundsMs[i]
			}
			return lower + (upper-lower)*(target-cumulative)/float64(count), true
		}
		cumulative = next
	}
	return durationBoundsMs[len(durationBoundsMs)-1], true
}

func schemaDTO() []SchemaFieldDTO {
	result := make([]SchemaFieldDTO, 0, len(accessFields))
	for _, field := range accessFields {
		aliases := make([]string, len(field.aliases))
		for i, alias := range field.aliases {
			aliases[i] = alias.name
		}
		result = append(result, SchemaFieldDTO{Name: field.name, Label: field.label, Aliases: aliases})
	}
	return result
}

// kindOf names the web server: from the service name the generated
// configs set, else the log format.
func kindOf(source *Source, dialect string) string {
	var identity sourceIdentity
	_ = json.Unmarshal(source.Identity, &identity)
	for _, candidate := range []string{identity.Service, identity.Name} {
		lower := strings.ToLower(candidate)
		for _, known := range []struct{ marker, kind string }{
			{"nginx", "Nginx"}, {"apache", "Apache HTTP Server"}, {"httpd", "Apache HTTP Server"},
			{"envoy", "Envoy"}, {"traefik", "Traefik"}, {"haproxy", "HAProxy"}, {"iis", "IIS"}, {"caddy", "Caddy"},
		} {
			if strings.Contains(lower, known.marker) {
				return known.kind
			}
		}
	}
	switch dialect {
	case "JSON (Envoy)":
		return "Envoy"
	case "JSON (Traefik)":
		return "Traefik"
	case "W3C (IIS)":
		return "IIS"
	case "OTel names":
		return "Custom (OTLP)"
	}
	return "Web server"
}

// whereOf says where a source runs.
func whereOf(identity sourceIdentity) string {
	var parts []string
	attributes := map[string]string{}
	for _, pair := range identity.Attributes {
		attributes[pair[0]] = pair[1]
	}
	cluster := firstNonEmpty(attributes["k8s_cluster_name"], identity.Cluster)
	namespace := firstNonEmpty(attributes["k8s_namespace_name"], identity.Namespace)
	if cluster != "" {
		parts = append(parts, cluster)
	}
	if namespace != "" {
		workload := ""
		for _, kind := range workloadKinds {
			if name := attributes[kind.attr]; name != "" {
				workload = " " + kind.kind
			}
		}
		parts = append(parts, namespace+workload)
	}
	if host := firstNonEmpty(attributes["host_name"], identity.HostName); host != "" && cluster == "" {
		parts = append(parts, host)
	}
	return strings.Join(parts, " · ")
}

func displayName(source *Source) string {
	if source.Name != "" {
		return source.Name
	}
	var identity sourceIdentity
	_ = json.Unmarshal(source.Identity, &identity)
	if identity.Name != "" {
		return identity.Name
	}
	return source.SourceKey
}

func retentionText(days int) string {
	if days == 0 {
		return "like the logs"
	}
	return fmt.Sprintf("%d days", days)
}

func seriesBucket(length time.Duration) time.Duration {
	switch {
	case length <= 48*time.Hour:
		return time.Hour
	case length <= 8*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func ceilHour(t time.Time) time.Time {
	floor := t.Truncate(time.Hour)
	if floor.Equal(t) {
		return t
	}
	return floor.Add(time.Hour)
}

func mergeInto[K comparable](target map[K]*totalsAgg, key K, agg *totalsAgg) {
	existing := target[key]
	if existing == nil {
		existing = newTotals()
		target[key] = existing
	}
	existing.merge(agg)
}

func contains(list []string, value string) bool {
	index := sort.SearchStrings(list, value)
	return index < len(list) && list[index] == value
}
