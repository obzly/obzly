package logs_querying

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"obzly/internal/authz"
	"obzly/internal/config"
	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// The Logs page's Patterns, Volume & noise and Health map tabs and its
// "Explain this spike" panel. Counts are grouped counts on the per-minute
// counts' dimensions, so they read logs_1m where segments have it (raw logs
// only for the uncompacted tail; Health map by host is the exception, as
// host_name is not a per-minute dimension). The merge picks the top keys, or
// the keys a comparison flags, in SQL: only those reach Go, however many
// patterns or pods there are.
const (
	insightCacheKeyPrefix = "logs:insight:"
	insightCacheTTL       = time.Minute
	// insightMaxRange bounds every range an insight reads.
	insightMaxRange = 31 * 24 * time.Hour
	// insightGroupLimit: the aggregation's row cap; reaching it marks a
	// response partial.
	insightGroupLimit = 10_000
	// trendNewCandidates: patterns absent 24 h earlier whose previous 7 days
	// are checked (by an IN filter, not a 7-day count of every pattern);
	// more mark the response partial and NewCount a lower bound.
	trendNewCandidates = 2_000

	trendMaxBuckets   = 60
	trendDefaultLimit = 30
	trendMaxLimit     = 50
	// trendMaxListed caps the patterns whose series are read: with
	// trendMaxBuckets (+1 for alignment) it stays under insightGroupLimit.
	trendMaxListed = 150
	// trendMinCount: fewest lines for a pattern to be new or spiking (or,
	// in the earlier window, dropping).
	trendMinCount   = 5
	trendMaxService = 5

	explainDefaultMinCount = 20
	explainMaxFields       = 12

	volumeDefaultDays   = 14
	volumeMaxDays       = 31
	volumeTopServices   = 6
	volumeNoisyPatterns = 10
	volumeNoisyDays     = 7

	healthMaxTiles = 150
	// healthMaxGroups: (group, workload, service) keys read, most lines
	// first; more mark the map partial.
	healthMaxGroups = 5_000

	day = 24 * time.Hour
)

var (
	trendBucketChoices = []time.Duration{
		time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour,
		3 * time.Hour, 6 * time.Hour, 12 * time.Hour, day,
	}
	errorLevels        = map[string]bool{"error": true, "fatal": true, "critical": true}
	warnLevels         = map[string]bool{"warn": true, "warning": true}
	generatedNamePart  = regexp.MustCompile(logs_core.GeneratedNamePartPattern)
	statefulSetOrdinal = regexp.MustCompile(logs_core.StatefulSetOrdinalPattern)
	// explainDimensions: each counted on its own; pods add their ReplicaSet.
	explainDimensions = []string{
		"service_name", "k8s_namespace_name", "k8s_container_name", "k8s_pod_name", "level",
		logs_core.ReplicaSetField,
	}
)

// counter runs an insight's aggregations, remembering whether any reached
// the row cap.
type counter struct {
	ctx       context.Context
	projectID uuid.UUID
	budget    int64
	partial   bool
}

// ExecutePatternTrends is the Patterns tab: pattern counts per bucket, with
// each pattern's level, services, sample and change against earlier windows.
func (s *LogQueryService) ExecutePatternTrends(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.PatternTrendsRequestDTO,
	user *users_models.User,
) (*logs_core.PatternTrendsResponseDTO, error) {
	release, err := s.beginInsight(ctx, projectID, user, request.Query, request.TimeRange)
	if err != nil {
		return nil, err
	}
	defer release()

	from, to := wholeMinuteRange(request.TimeRange)
	normalized := *request
	normalized.TimeRange = &logs_core.TimeRangeDTO{From: &from, To: &to}
	normalized.BucketMs = trendBucket(request.BucketMs, to.Sub(from)).Milliseconds()
	normalized.Limit = clampInt(request.Limit, trendDefaultLimit, trendMaxLimit)
	return cachedInsight(
		ctx,
		s,
		insightKey("trends", projectID, normalized),
		func() (*logs_core.PatternTrendsResponseDTO, error) {
			return s.patternTrends(ctx, projectID, &normalized)
		},
	)
}

// ExecuteExplain lists the field values over-represented in the current
// range against the baseline.
func (s *LogQueryService) ExecuteExplain(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.ExplainRequestDTO,
	user *users_models.User,
) (*logs_core.ExplainResponseDTO, error) {
	release, err := s.beginInsight(ctx, projectID, user, request.Query, request.Current, request.Baseline)
	if err != nil {
		return nil, err
	}
	defer release()

	minCount := request.MinCount
	if minCount <= 0 {
		minCount = explainDefaultMinCount
	}
	c := s.newCounter(ctx, projectID)
	// A value's gain: its share of the current lines less its baseline share.
	gain := "(w0::DOUBLE / NULLIF(t0, 0) - COALESCE(w1::DOUBLE / NULLIF(t1, 0), 0))"
	counted, err := c.count(&logs_core.GroupWindowsRequestDTO{
		Query: request.Query, GroupSets: explainDimensions,
		Windows: []logs_core.CountWindowDTO{
			{From: *request.Current.From, To: *request.Current.To},
			{From: *request.Baseline.From, To: *request.Baseline.To},
		},
		Selections: []logs_core.GroupSelectionDTO{{
			Where:     "NOT ends_with(key, chr(31)) AND w0 >= ? AND " + gain + " > 0",
			WhereArgs: []any{minCount}, OrderBy: gain + " DESC", Limit: explainMaxFields,
		}},
	})
	if err != nil {
		return nil, err
	}
	curTotal, baseTotal := counted.Totals[0], counted.Totals[1]

	response := &logs_core.ExplainResponseDTO{
		CurrentTotal: curTotal, BaselineTotal: baseTotal, Fields: []logs_core.ExplainFieldDTO{}, Partial: c.partial,
	}
	type scored struct {
		field logs_core.ExplainFieldDTO
		gain  float64
	}
	var candidates []scored
	for _, row := range counted.Selections[0].Rows {
		parts := logs_core.SplitGroupKey(row.Key, 2)
		dimension, err := strconv.Atoi(parts[0])
		if err != nil || dimension < 0 || dimension >= len(explainDimensions) {
			continue
		}
		field := logs_core.ExplainFieldDTO{
			Field: explainDimensions[dimension], Value: parts[1], Current: row.Counts[0], Baseline: row.Counts[1],
			CurrentShare: float64(row.Counts[0]) / float64(curTotal),
		}
		if baseTotal > 0 {
			field.BaselineShare = float64(field.Baseline) / float64(baseTotal)
		}
		candidates = append(candidates, scored{field, field.CurrentShare - field.BaselineShare})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].gain != candidates[j].gain {
			return candidates[i].gain > candidates[j].gain
		}
		if candidates[i].field.Field != candidates[j].field.Field {
			return candidates[i].field.Field < candidates[j].field.Field
		}
		return candidates[i].field.Value < candidates[j].field.Value
	})
	for _, candidate := range candidates {
		response.Fields = append(response.Fields, candidate.field)
	}
	return response, nil
}

// ExecuteVolume is the Volume & noise tab: lines per day by service, by
// level, today against yesterday, bytes per line and the noisiest patterns.
func (s *LogQueryService) ExecuteVolume(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.VolumeRequestDTO,
	user *users_models.User,
) (*logs_core.VolumeResponseDTO, error) {
	release, err := s.beginInsight(ctx, projectID, user, nil)
	if err != nil {
		return nil, err
	}
	defer release()

	days := clampInt(request.Days, volumeDefaultDays, volumeMaxDays)
	now := time.Now().UTC().Truncate(time.Minute)
	return cachedInsight(
		ctx,
		s,
		insightKey("volume", projectID, []any{days, now}),
		func() (*logs_core.VolumeResponseDTO, error) {
			return s.volume(ctx, projectID, days, now)
		},
	)
}

// ExecuteHealthMap is the Health map tab: lines, error and warning lines per
// workload (or service), grouped by namespace (or host).
func (s *LogQueryService) ExecuteHealthMap(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.HealthMapRequestDTO,
	user *users_models.User,
) (*logs_core.HealthMapResponseDTO, error) {
	switch request.GroupBy {
	case logs_core.HealthMapByKubernetes, logs_core.HealthMapByHost, logs_core.HealthMapByService:
	default:
		return nil, &ValidationError{
			Code: logs_core.ErrorInvalidQueryStructure, Message: `groupBy must be "k8s", "host" or "service"`,
		}
	}
	release, err := s.beginInsight(ctx, projectID, user, request.Query, request.TimeRange)
	if err != nil {
		return nil, err
	}
	defer release()

	from, to := wholeMinuteRange(request.TimeRange)
	normalized := *request
	normalized.TimeRange = &logs_core.TimeRangeDTO{From: &from, To: &to}
	return cachedInsight(
		ctx,
		s,
		insightKey("health", projectID, normalized),
		func() (*logs_core.HealthMapResponseDTO, error) {
			return s.healthMap(ctx, projectID, &normalized)
		},
	)
}

// WorkloadOfPod is a pod's workload: its name without the generated parts
// (Deployment: <name>-<hash>-<suffix>, DaemonSet or Job: <name>-<suffix>,
// StatefulSet: <name>-<ordinal>).
func WorkloadOfPod(pod string) string {
	if generatedNamePart.MatchString(pod) {
		name := generatedNamePart.ReplaceAllString(pod, "")
		if generatedNamePart.MatchString(name) {
			name = generatedNamePart.ReplaceAllString(name, "")
		}
		return name
	}
	if statefulSetOrdinal.MatchString(pod) {
		return statefulSetOrdinal.ReplaceAllString(pod, "")
	}
	return pod
}

// ReplicaSetOfPod is a Deployment pod's ReplicaSet (<name>-<hash>), "" for
// any other pod.
func ReplicaSetOfPod(pod string) string {
	if !generatedNamePart.MatchString(pod) {
		return ""
	}
	replicaSet := generatedNamePart.ReplaceAllString(pod, "")
	if !generatedNamePart.MatchString(replicaSet) {
		return ""
	}
	return replicaSet
}

func (s *LogQueryService) patternTrends(
	ctx context.Context, projectID uuid.UUID, request *logs_core.PatternTrendsRequestDTO,
) (*logs_core.PatternTrendsResponseDTO, error) {
	from, to := *request.TimeRange.From, *request.TimeRange.To
	end := to.Add(-time.Nanosecond)
	bucket := time.Duration(request.BucketMs) * time.Millisecond
	c := s.newCounter(ctx, projectID)
	hashField := logs_core.PatternHashField

	// Each pattern's lines now (w0) and 24 h earlier (w1), compared in the
	// merge: the biggest, the new candidates, the spiking and the dropping
	// patterns come back, and how many of each.
	named := "length(key) > 0"
	counted, err := c.count(&logs_core.GroupWindowsRequestDTO{
		Query: request.Query, GroupField: hashField,
		Windows: []logs_core.CountWindowDTO{{From: from, To: end}, {From: from.Add(-day), To: end.Add(-day)}},
		Selections: []logs_core.GroupSelectionDTO{
			{Where: named + " AND w0 > 0", Limit: request.Limit},
			{
				Where: named + " AND w1 = 0 AND w0 >= ?", WhereArgs: []any{trendMinCount},
				Limit: trendNewCandidates,
			},
			{
				Where: named + " AND w1 > 0 AND w0 >= ? AND w0 >= 2 * w1", WhereArgs: []any{trendMinCount},
				OrderBy: "w0 / w1 DESC", Limit: request.Limit,
			},
			{
				Where: named + " AND w1 >= ? AND 2 * w0 <= w1", WhereArgs: []any{trendMinCount},
				OrderBy: "w1 DESC", Limit: request.Limit,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	current, yesterday := map[string]int64{}, map[string]int64{}
	keysOf := func(selection logs_core.GroupSelectionResultDTO) []string {
		keys := make([]string, 0, len(selection.Rows))
		for _, row := range selection.Rows {
			current[row.Key], yesterday[row.Key] = row.Counts[0], row.Counts[1]
			keys = append(keys, row.Key)
		}
		return keys
	}
	top, candidates := keysOf(counted.Selections[0]), keysOf(counted.Selections[1])
	rising, falling := keysOf(counted.Selections[2]), keysOf(counted.Selections[3])

	// The 7 days before the range, only for the patterns that need it: a
	// candidate is new only without any line there.
	checked := unique(candidates, top, rising, falling)
	week := map[string]int64{}
	if len(checked) > 0 {
		weekCounts, err := c.count(&logs_core.GroupWindowsRequestDTO{
			Query: request.Query, GroupField: hashField, Keys: checked,
			Windows:    []logs_core.CountWindowDTO{{From: from.Add(-7 * day), To: from.Add(-time.Nanosecond)}},
			Selections: []logs_core.GroupSelectionDTO{{Where: "w0 > 0", Limit: len(checked)}},
		})
		if err != nil {
			return nil, err
		}
		for _, row := range weekCounts.Selections[0].Rows {
			week[row.Key] = row.Counts[0]
		}
	}
	var added []string
	for _, hash := range candidates {
		if week[hash] == 0 {
			added = append(added, hash)
		}
	}
	if counted.Selections[1].Truncated() {
		c.partial = true
	}

	response := &logs_core.PatternTrendsResponseDTO{
		BucketMs: request.BucketMs, Patterns: []logs_core.PatternTrendDTO{},
		Total: counted.Totals[0], Distinct: int(counted.Selections[0].Matched),
		NewCount: len(added), SpikeCount: int(counted.Selections[2].Matched),
		DropCount: int(counted.Selections[3].Matched),
	}
	for start := from.Truncate(bucket); start.Before(to); start = start.Add(bucket) {
		response.Buckets = append(response.Buckets, start)
	}
	listed := []string{}
	seen := map[string]bool{}
	for _, group := range [][]string{top, added, rising, falling} {
		for _, hash := range group[:min(len(group), request.Limit)] {
			if !seen[hash] && len(listed) < trendMaxListed {
				seen[hash] = true
				listed = append(listed, hash)
			}
		}
	}
	changes := map[string]string{}
	for _, hash := range listed {
		changes[hash] = trendChange(current[hash], yesterday[hash], week[hash])
	}
	if len(listed) == 0 {
		response.Other = make([]int64, len(response.Buckets))
		response.Partial = c.partial
		return response, nil
	}

	series, err := c.series(andQuery(request.Query, inCondition(hashField, listed)), hashField, from, end, bucket)
	if err != nil {
		return nil, err
	}
	totals, err := c.series(request.Query, "", from, end, bucket)
	if err != nil {
		return nil, err
	}
	bucketIndex := map[int64]int{}
	for i, start := range response.Buckets {
		bucketIndex[start.UnixMilli()] = i
	}
	response.Other = make([]int64, len(response.Buckets))
	for at, count := range totals[""] {
		if i, ok := bucketIndex[at]; ok {
			response.Other[i] += count
		}
	}

	var present, absent []string
	for _, hash := range listed {
		if current[hash] > 0 {
			present = append(present, hash)
		} else {
			absent = append(absent, hash)
		}
	}
	meta := map[string]*patternMeta{}
	samples := map[string]logs_core.PatternSampleDTO{}
	describe := func(hashes []string, from, to time.Time) error {
		if len(hashes) == 0 {
			return nil
		}
		rows, err := c.top(andQuery(request.Query, inCondition(hashField, hashes)),
			logs_core.JoinGroupFields(hashField, "level", "service_name"), from, to, insightGroupLimit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			parts, count := logs_core.SplitGroupKey(row.Key, 3), row.Counts[0]
			entry := meta[parts[0]]
			if entry == nil {
				entry = &patternMeta{levels: map[string]int64{}, services: map[string]int64{}}
				meta[parts[0]] = entry
			}
			entry.levels[parts[1]] += count
			if parts[2] != "" {
				entry.services[parts[2]] += count
			}
		}
		found, err := logs_core.GetLogCoreRepository().ExecutePatternSamples(ctx, projectID,
			&logs_core.PatternSamplesRequestDTO{
				Query: request.Query, TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to}, Hashes: hashes,
			})
		if err != nil {
			return err
		}
		for hash, sample := range found {
			samples[hash] = sample
		}
		return nil
	}
	if err := describe(present, from, end); err != nil {
		return nil, err
	}
	if err := describe(absent, from.Add(-day), end.Add(-day)); err != nil {
		return nil, err
	}

	for _, hash := range listed {
		trend := logs_core.PatternTrendDTO{
			Hash: hash, Count: current[hash], Yesterday: yesterday[hash], PreviousWeek: week[hash],
			Change: changes[hash], Series: make([]int64, len(response.Buckets)), Services: []string{},
		}
		for at, count := range series[hash] {
			if i, ok := bucketIndex[at]; ok {
				trend.Series[i] += count
				response.Other[i] -= count
			}
		}
		if sample, ok := samples[hash]; ok {
			trend.Pattern, trend.Sample, trend.Level = sample.Pattern, sample.Sample, sample.Level
		}
		if entry := meta[hash]; entry != nil {
			if level := topKeys(entry.levels, 1); len(level) > 0 {
				trend.Level = level[0]
			}
			trend.Services = topKeys(entry.services, trendMaxService)
		}
		response.Patterns = append(response.Patterns, trend)
	}
	for i := range response.Other {
		response.Other[i] = max(response.Other[i], 0)
	}
	response.Partial = c.partial
	return response, nil
}

type patternMeta struct {
	levels, services map[string]int64
}

func (s *LogQueryService) volume(
	ctx context.Context, projectID uuid.UUID, days int, now time.Time,
) (*logs_core.VolumeResponseDTO, error) {
	// The caller clamps already; bound it here too, where the slices are sized.
	if days < 1 || days > volumeMaxDays {
		days = volumeDefaultDays
	}
	c := s.newCounter(ctx, projectID)
	today := now.Truncate(day)
	start := today.Add(-time.Duration(days-1) * day)
	weekAgo := now.Add(-volumeNoisyDays * day)
	response := &logs_core.VolumeResponseDTO{
		Services: []logs_core.VolumeSeriesDTO{}, Levels: []logs_core.VolumeCountDTO{},
		Noisy: []logs_core.NoisyPatternDTO{},
	}
	// One window per day: the biggest services' days, and every line's.
	// Sized by the constant cap, not by days: days comes from the request.
	windows := make([]logs_core.CountWindowDTO, 0, volumeMaxDays)
	sums := make([]string, 0, volumeMaxDays)
	for i := range days {
		at := start.Add(time.Duration(i) * day)
		response.Days = append(response.Days, at)
		windows = append(windows, logs_core.CountWindowDTO{From: at, To: minTime(at.Add(day-time.Nanosecond), now)})
		sums = append(sums, "w"+strconv.Itoa(i))
	}
	perService, err := c.count(&logs_core.GroupWindowsRequestDTO{
		GroupField: "service_name", Windows: windows,
		Selections: []logs_core.GroupSelectionDTO{
			{OrderBy: strings.Join(sums, " + ") + " DESC", Limit: volumeTopServices},
		},
	})
	if err != nil {
		return nil, err
	}
	perDay := newDayCounts(days)
	copy(perDay, perService.Totals)
	other := logs_core.VolumeSeriesDTO{Name: "other", Counts: newDayCounts(days), Other: true}
	copy(other.Counts, perDay)
	for _, count := range perDay {
		other.Total += count
	}
	for _, service := range perService.Selections[0].Rows {
		if service.Key == "" {
			continue
		}
		row := logs_core.VolumeSeriesDTO{Name: service.Key, Counts: newDayCounts(days)}
		for i, count := range service.Counts {
			row.Counts[i] = count
			row.Total += count
			other.Counts[i] -= count
		}
		other.Total -= row.Total
		response.Services = append(response.Services, row)
	}
	if other.Total > 0 {
		response.Services = append(response.Services, other)
	}

	levels, err := c.top(nil, "level", weekAgo, now, insightGroupLimit)
	if err != nil {
		return nil, err
	}
	for _, level := range levels {
		response.Levels = append(response.Levels, logs_core.VolumeCountDTO{Name: level.Key, Count: level.Counts[0]})
	}
	soFar, err := c.count(&logs_core.GroupWindowsRequestDTO{Windows: []logs_core.CountWindowDTO{
		{From: today, To: now.Add(-time.Nanosecond)},
		{From: today.Add(-day), To: now.Add(-day - time.Nanosecond)},
	}})
	if err != nil {
		return nil, err
	}
	response.Today, response.YesterdaySoFar = soFar.Totals[0], soFar.Totals[1]

	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read log stats: %w", err)
	}
	if stats != nil && stats.TotalLogs > 0 {
		response.StoredBytes = int64(stats.TotalSizeMB * 1024 * 1024)
		response.BytesPerLine = stats.TotalSizeMB * 1024 * 1024 / float64(stats.TotalLogs)
		response.RawBytesPerLine = stats.RawSizeMB * 1024 * 1024 / float64(stats.TotalLogs)
	}

	noisy, err := c.count(&logs_core.GroupWindowsRequestDTO{
		GroupField: logs_core.JoinGroupFields(logs_core.PatternHashField, "service_name", "level"),
		Windows:    []logs_core.CountWindowDTO{{From: weekAgo, To: now}},
		Selections: []logs_core.GroupSelectionDTO{
			{Where: "length(split_part(key, chr(31), 1)) > 0", Limit: volumeNoisyPatterns},
		},
	})
	if err != nil {
		return nil, err
	}
	activeDays := 0
	for i := max(days-volumeNoisyDays, 0); i < days; i++ {
		if perDay[i] > 0 {
			activeDays++
		}
	}
	activeDays = max(activeDays, 1)
	var hashes []string
	seen := map[string]bool{}
	for _, row := range noisy.Selections[0].Rows {
		parts := logs_core.SplitGroupKey(row.Key, 3)
		response.Noisy = append(response.Noisy, logs_core.NoisyPatternDTO{
			Hash: parts[0], Service: parts[1], Level: parts[2], Count: row.Counts[0],
			PerDay: float64(row.Counts[0]) / float64(activeDays),
		})
		if !seen[parts[0]] {
			seen[parts[0]] = true
			hashes = append(hashes, parts[0])
		}
	}
	if len(hashes) > 0 {
		samples, err := logs_core.GetLogCoreRepository().ExecutePatternSamples(ctx, projectID,
			&logs_core.PatternSamplesRequestDTO{
				TimeRange: &logs_core.TimeRangeDTO{From: &weekAgo, To: &now}, Hashes: hashes,
			})
		if err != nil {
			return nil, err
		}
		for i := range response.Noisy {
			if sample, ok := samples[response.Noisy[i].Hash]; ok {
				response.Noisy[i].Pattern, response.Noisy[i].Sample = sample.Pattern, sample.Sample
			}
		}
	}
	response.Partial = c.partial
	return response, nil
}

func (s *LogQueryService) healthMap(
	ctx context.Context, projectID uuid.UUID, request *logs_core.HealthMapRequestDTO,
) (*logs_core.HealthMapResponseDTO, error) {
	from, to := *request.TimeRange.From, *request.TimeRange.To
	end := to.Add(-time.Nanosecond)
	var fields []string
	var place func(values []string) (group, tile string)
	unknown := func(value, fallback string) string {
		if value == "" {
			return fallback
		}
		return value
	}
	switch request.GroupBy {
	case logs_core.HealthMapByKubernetes:
		// Pods are grouped into workloads in SQL (WorkloadOfPod there).
		fields = []string{"k8s_namespace_name", logs_core.WorkloadField, "service_name"}
		place = func(values []string) (string, string) {
			tile := unknown(values[2], "(unknown)")
			if values[1] != "" {
				tile = values[1]
			}
			return unknown(values[0], "(outside Kubernetes)"), tile
		}
	case logs_core.HealthMapByHost:
		fields = []string{"host_name", "service_name"}
		place = func(values []string) (string, string) {
			return unknown(values[0], "(no host)"), unknown(values[1], "(unknown)")
		}
	default:
		fields = []string{"service_name"}
		place = func(values []string) (string, string) {
			return "all services", unknown(values[0], "(unknown)")
		}
	}

	// Lines, error lines and warning lines in the range, and lines 24 h
	// earlier, per key - the most lines first, at most healthMaxGroups.
	c := s.newCounter(ctx, projectID)
	counted, err := c.count(&logs_core.GroupWindowsRequestDTO{
		Query: request.Query, GroupField: logs_core.JoinGroupFields(fields...),
		Windows: []logs_core.CountWindowDTO{
			{From: from, To: end},
			{From: from, To: end, Levels: mapKeys(errorLevels)},
			{From: from, To: end, Levels: mapKeys(warnLevels)},
			{From: from.Add(-day), To: end.Add(-day)},
		},
		Selections: []logs_core.GroupSelectionDTO{{Where: "w0 > 0", Limit: healthMaxGroups}},
	})
	if err != nil {
		return nil, err
	}
	grouped := counted.Selections[0]

	type tileKey struct{ group, tile string }
	tiles := map[tileKey]*logs_core.HealthTileDTO{}
	for _, row := range grouped.Rows {
		group, name := place(logs_core.SplitGroupKey(row.Key, len(fields)))
		key := tileKey{group, name}
		if tiles[key] == nil {
			tiles[key] = &logs_core.HealthTileDTO{Name: name}
		}
		tile := tiles[key]
		tile.Total += row.Counts[0]
		tile.Errors += row.Counts[1]
		tile.Warnings += row.Counts[2]
		tile.Previous += row.Counts[3]
	}

	keys := make([]tileKey, 0, len(tiles))
	for key := range tiles {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if tiles[keys[i]].Total != tiles[keys[j]].Total {
			return tiles[keys[i]].Total > tiles[keys[j]].Total
		}
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		return keys[i].tile < keys[j].tile
	})
	response := &logs_core.HealthMapResponseDTO{GroupBy: request.GroupBy, Groups: []logs_core.HealthGroupDTO{}}
	if len(keys) > healthMaxTiles {
		response.HiddenTiles = len(keys) - healthMaxTiles
		keys = keys[:healthMaxTiles]
	}
	if grouped.Truncated() {
		// Each key left out is about one tile (a workload's services split it).
		response.HiddenTiles += int(grouped.Matched) - len(grouped.Rows)
		c.partial = true
	}
	groupIndex := map[string]int{}
	for _, key := range keys {
		i, ok := groupIndex[key.group]
		if !ok {
			i = len(response.Groups)
			groupIndex[key.group] = i
			response.Groups = append(response.Groups, logs_core.HealthGroupDTO{
				Name: key.group, Tiles: []logs_core.HealthTileDTO{},
			})
		}
		response.Groups[i].Total += tiles[key].Total
		response.Groups[i].Tiles = append(response.Groups[i].Tiles, *tiles[key])
	}
	sort.SliceStable(response.Groups, func(i, j int) bool {
		return response.Groups[i].Total > response.Groups[j].Total
	})
	response.Partial = c.partial
	return response, nil
}

// beginInsight takes a concurrency slot (release frees it), checks access
// and validates the query and ranges: each needs From and To and at most
// insightMaxRange between them.
func (s *LogQueryService) beginInsight(
	ctx context.Context,
	projectID uuid.UUID,
	user *users_models.User,
	query *logs_core.QueryNode,
	ranges ...*logs_core.TimeRangeDTO,
) (func(), error) {
	invalid := func(message string) error {
		return &ValidationError{Code: logs_core.ErrorInvalidQueryStructure, Message: message}
	}
	for _, timeRange := range ranges {
		if err := s.validateTimeRange(timeRange); err != nil {
			return nil, err
		}
		if timeRange.From == nil {
			return nil, invalid("timeRange.from is required")
		}
		if timeRange.To.Sub(*timeRange.From) > insightMaxRange {
			return nil, invalid("the time range can be at most 31 days")
		}
	}
	queryID := uuid.New().String()
	if err := s.concurrentQueryLimiter.AcquireQuerySlot(user.ID, queryID); err != nil {
		return nil, err
	}
	release := func() { s.concurrentQueryLimiter.ReleaseQuerySlot(user.ID, queryID) }
	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		release()
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		release()
		return nil, errors.New("insufficient permissions to query project logs")
	}
	if err := s.queryValidator.ValidateQuery(query); err != nil {
		release()
		return nil, fmt.Errorf("invalid query structure: %w", err)
	}
	return release, nil
}

func (s *LogQueryService) newCounter(ctx context.Context, projectID uuid.UUID) *counter {
	return &counter{ctx: ctx, projectID: projectID, budget: config.GetEnv().QueryMaxScanBytes}
}

// count runs a grouped count under the insight's scan budget.
func (c *counter) count(request *logs_core.GroupWindowsRequestDTO) (*logs_core.GroupWindowsResponseDTO, error) {
	request.ScanBudget = c.budget
	return logs_core.GetLogCoreRepository().ExecuteGroupWindows(c.ctx, c.projectID, request)
}

// top is the at most limit keys of groupField with the most lines matching
// filter in [from, to], most first (ties by key); leaving any out marks the
// insight partial.
func (c *counter) top(
	filter *logs_core.QueryNode, groupField string, from, to time.Time, limit int,
) ([]logs_core.GroupCountsDTO, error) {
	counted, err := c.count(&logs_core.GroupWindowsRequestDTO{
		Query: filter, GroupField: groupField, Windows: []logs_core.CountWindowDTO{{From: from, To: to}},
		Selections: []logs_core.GroupSelectionDTO{{Where: "w0 > 0", Limit: limit}},
	})
	if err != nil {
		return nil, err
	}
	if counted.Selections[0].Truncated() {
		c.partial = true
	}
	return counted.Selections[0].Rows, nil
}

// series is the lines matching filter in [from, to] per group key and
// bucket start (Unix ms); bucket is whole minutes.
func (c *counter) series(
	filter *logs_core.QueryNode, groupField string, from, to time.Time, bucket time.Duration,
) (map[string]map[int64]int64, error) {
	response, err := logs_core.GetLogCoreRepository().ExecuteAggregation(c.ctx, c.projectID,
		&logs_core.AggregationRequestDTO{
			Query: filter, TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
			GroupField: groupField, BucketMs: bucket.Milliseconds(), Limit: insightGroupLimit,
			ScanBudget: c.budget,
		})
	if err != nil {
		return nil, err
	}
	if len(response.Buckets) >= insightGroupLimit {
		c.partial = true
	}
	series := map[string]map[int64]int64{}
	for _, bucket := range response.Buckets {
		if series[bucket.GroupKey] == nil {
			series[bucket.GroupKey] = map[int64]int64{}
		}
		at := int64(0)
		if bucket.Timestamp != nil {
			at = bucket.Timestamp.UnixMilli()
		}
		series[bucket.GroupKey][at] += bucket.Count
	}
	return series, nil
}

// cachedInsight is compute's answer, cached for insightCacheTTL under key.
func cachedInsight[T any](
	ctx context.Context, s *LogQueryService, key string, compute func() (*T, error),
) (*T, error) {
	if s.cache != nil {
		if cached, err := s.cache.Do(ctx, s.cache.B().Get().Key(key).Build()).AsBytes(); err == nil {
			var response T
			if json.Unmarshal(cached, &response) == nil {
				return &response, nil
			}
		}
	}
	response, err := compute()
	if err != nil || s.cache == nil {
		return response, err
	}
	if encoded, err := json.Marshal(response); err == nil {
		if err := s.cache.Do(ctx, s.cache.B().Set().Key(key).Value(string(encoded)).
			Ex(insightCacheTTL).Build()).Error(); err != nil {
			s.logger.Warn("failed to cache log insight", slog.String("error", err.Error()))
		}
	}
	return response, nil
}

func insightKey(kind string, projectID uuid.UUID, request any) string {
	encoded, _ := json.Marshal(request)
	sum := sha256.Sum256(encoded)
	return insightCacheKeyPrefix + kind + ":" + projectID.String() + ":" + hex.EncodeToString(sum[:])
}

// wholeMinuteRange is a range cut to whole minutes ([from's minute, to's
// minute)), at least one minute long.
func wholeMinuteRange(timeRange *logs_core.TimeRangeDTO) (time.Time, time.Time) {
	from := timeRange.From.UTC().Truncate(time.Minute)
	to := timeRange.To.UTC().Truncate(time.Minute)
	if !to.After(from) {
		to = from.Add(time.Minute)
	}
	return from, to
}

// trendBucket is requested rounded up to whole minutes, or the smallest
// choice giving at most trendMaxBuckets over span; never more buckets.
func trendBucket(requestedMs int64, span time.Duration) time.Duration {
	requested := time.Duration(requestedMs) * time.Millisecond
	if requested > 0 {
		requested = (requested + time.Minute - 1).Truncate(time.Minute)
		if span/requested <= trendMaxBuckets {
			return requested
		}
	}
	for _, choice := range trendBucketChoices {
		if span/choice <= trendMaxBuckets {
			return choice
		}
	}
	return trendBucketChoices[len(trendBucketChoices)-1]
}

func trendChange(current, yesterday, week int64) string {
	switch {
	case yesterday == 0 && week == 0 && current >= trendMinCount:
		return logs_core.PatternChangeNew
	case yesterday > 0 && current >= trendMinCount && current >= 2*yesterday:
		return logs_core.PatternChangeSpike
	case yesterday >= trendMinCount && 2*current <= yesterday:
		return logs_core.PatternChangeDrop
	}
	return ""
}

func andQuery(nodes ...*logs_core.QueryNode) *logs_core.QueryNode {
	children := []logs_core.QueryNode{}
	for _, node := range nodes {
		if node != nil {
			children = append(children, *node)
		}
	}
	switch len(children) {
	case 0:
		return nil
	case 1:
		return &children[0]
	}
	return &logs_core.QueryNode{
		Type:  logs_core.QueryNodeTypeLogical,
		Logic: &logs_core.LogicalNode{Operator: logs_core.LogicalOperatorAnd, Children: children},
	}
}

func inCondition(field string, values []string) *logs_core.QueryNode {
	list := make([]any, len(values))
	for i, value := range values {
		list[i] = value
	}
	return &logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field: field, Operator: logs_core.ConditionOperatorIn, Value: list,
		},
	}
}

// unique is lists' values, each once, in order.
func unique(lists ...[]string) []string {
	seen := map[string]bool{}
	var values []string
	for _, list := range lists {
		for _, value := range list {
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
	}
	return values
}

// mapKeys is set's keys, sorted.
func mapKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// topKeys is counts' n biggest keys, most first, ties by key.
func topKeys(counts map[string]int64, n int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:min(len(keys), n)]
}

func clampInt(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

// newDayCounts allocates the fixed maximum and slices it, so the request never sizes an
// allocation; volume has already bounded days to 1..volumeMaxDays.
func newDayCounts(days int) []int64 {
	return make([]int64, volumeMaxDays)[:days]
}
