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
	"strings"
	"time"

	"obzly/internal/authz"
	"obzly/internal/config"
	logs_core "obzly/internal/features/logs/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// The Logs page's Patterns, Volume & noise and Health map tabs and its
// "Explain this spike" panel. Every count is a bucketed aggregation over
// whole minutes on the per-minute counts' dimensions, so it reads logs_1m
// where segments have it (raw logs only for the uncompacted tail); Health
// map by host is the exception (host_name is not a per-minute dimension).
const (
	insightCacheKeyPrefix = "logs:insight:"
	insightCacheTTL       = time.Minute
	// insightMaxRange bounds every range an insight reads.
	insightMaxRange = 31 * 24 * time.Hour
	// insightGroupLimit: the aggregation's row cap; reaching it marks a
	// response partial.
	insightGroupLimit = 10_000

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

	replicaSetField = "k8s_replicaset"
	day             = 24 * time.Hour
)

var (
	trendBucketChoices = []time.Duration{
		time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour,
		3 * time.Hour, 6 * time.Hour, 12 * time.Hour, day,
	}
	errorLevels = map[string]bool{"error": true, "fatal": true, "critical": true}
	warnLevels  = map[string]bool{"warn": true, "warning": true}
	// generatedNamePart is a Kubernetes generated name part (pod suffix or
	// pod-template-hash): 5-10 characters of the alphabet Kubernetes uses
	// for them, which has no vowels - so "indexer" isn't one.
	generatedNamePart  = regexp.MustCompile(`-[bcdfghjklmnpqrstvwxz2456789]{5,10}$`)
	statefulSetOrdinal = regexp.MustCompile(`-[0-9]+$`)
	explainDimensions  = []string{
		"service_name", "k8s_namespace_name", "k8s_container_name", "k8s_pod_name", "level",
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
	group := logs_core.JoinGroupFields(explainDimensions...)
	current, err := c.by(request.Query, group, *request.Current.From, *request.Current.To)
	if err != nil {
		return nil, err
	}
	baseline, err := c.by(request.Query, group, *request.Baseline.From, *request.Baseline.To)
	if err != nil {
		return nil, err
	}
	curValues, curTotal := explainMarginals(current)
	baseValues, baseTotal := explainMarginals(baseline)

	response := &logs_core.ExplainResponseDTO{
		CurrentTotal: curTotal, BaselineTotal: baseTotal, Fields: []logs_core.ExplainFieldDTO{}, Partial: c.partial,
	}
	type scored struct {
		field logs_core.ExplainFieldDTO
		gain  float64
	}
	var candidates []scored
	for key, count := range curValues {
		if count < minCount || curTotal == 0 {
			continue
		}
		field := logs_core.ExplainFieldDTO{
			Field: key[0], Value: key[1], Current: count, Baseline: baseValues[key],
			CurrentShare: float64(count) / float64(curTotal),
		}
		if baseTotal > 0 {
			field.BaselineShare = float64(field.Baseline) / float64(baseTotal)
		}
		if gain := field.CurrentShare - field.BaselineShare; gain > 0 {
			candidates = append(candidates, scored{field, gain})
		}
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
	for _, candidate := range candidates[:min(len(candidates), explainMaxFields)] {
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

	current, err := c.by(request.Query, hashField, from, end)
	if err != nil {
		return nil, err
	}
	yesterday, err := c.by(request.Query, hashField, from.Add(-day), end.Add(-day))
	if err != nil {
		return nil, err
	}
	week, err := c.by(request.Query, hashField, from.Add(-7*day), from.Add(-time.Nanosecond))
	if err != nil {
		return nil, err
	}

	response := &logs_core.PatternTrendsResponseDTO{
		BucketMs: request.BucketMs, Patterns: []logs_core.PatternTrendDTO{},
	}
	for start := from.Truncate(bucket); start.Before(to); start = start.Add(bucket) {
		response.Buckets = append(response.Buckets, start)
	}
	changes := map[string]string{}
	var added, rising, falling []string
	for hash, count := range current {
		response.Total += count
		if hash != "" {
			response.Distinct++
		}
	}
	for _, hash := range unionKeys(current, yesterday) {
		if hash == "" {
			continue
		}
		change := trendChange(current[hash], yesterday[hash], week[hash])
		changes[hash] = change
		switch change {
		case logs_core.PatternChangeNew:
			added = append(added, hash)
		case logs_core.PatternChangeSpike:
			rising = append(rising, hash)
		case logs_core.PatternChangeDrop:
			falling = append(falling, hash)
		}
	}
	response.NewCount, response.SpikeCount, response.DropCount = len(added), len(rising), len(falling)

	byCount := func(counts map[string]int64, hashes []string) {
		sort.Slice(hashes, func(i, j int) bool {
			if counts[hashes[i]] != counts[hashes[j]] {
				return counts[hashes[i]] > counts[hashes[j]]
			}
			return hashes[i] < hashes[j]
		})
	}
	top := make([]string, 0, len(current))
	for hash := range current {
		if hash != "" {
			top = append(top, hash)
		}
	}
	byCount(current, top)
	byCount(current, added)
	sort.Slice(rising, func(i, j int) bool {
		ri := float64(current[rising[i]]) / float64(yesterday[rising[i]])
		rj := float64(current[rising[j]]) / float64(yesterday[rising[j]])
		if ri != rj {
			return ri > rj
		}
		return rising[i] < rising[j]
	})
	byCount(yesterday, falling)
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
		counts, err := c.by(andQuery(request.Query, inCondition(hashField, hashes)),
			logs_core.JoinGroupFields(hashField, "level", "service_name"), from, to)
		if err != nil {
			return err
		}
		for key, count := range counts {
			parts := logs_core.SplitGroupKey(key, 3)
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
	dayIndex := map[int64]int{}
	for i := range days {
		at := start.Add(time.Duration(i) * day)
		response.Days = append(response.Days, at)
		dayIndex[at.UnixMilli()] = i
	}

	perService, err := c.series(nil, "service_name", start, now, day)
	if err != nil {
		return nil, err
	}
	totals := map[string]int64{}
	perDay := newDayCounts(days)
	for service, counts := range perService {
		for at, count := range counts {
			totals[service] += count
			if i, ok := dayIndex[at]; ok {
				perDay[i] += count
			}
		}
	}
	other := logs_core.VolumeSeriesDTO{Name: "other", Counts: newDayCounts(days), Other: true}
	topServices := topKeys(totals, len(totals))
	for rank, service := range topServices {
		row := logs_core.VolumeSeriesDTO{Name: service, Counts: newDayCounts(days), Total: totals[service]}
		target := &row
		if rank >= volumeTopServices || service == "" {
			target = &other
			other.Total += totals[service]
		}
		for at, count := range perService[service] {
			if i, ok := dayIndex[at]; ok {
				target.Counts[i] += count
			}
		}
		if target == &row {
			response.Services = append(response.Services, row)
		}
	}
	if other.Total > 0 {
		response.Services = append(response.Services, other)
	}

	levels, err := c.by(nil, "level", weekAgo, now)
	if err != nil {
		return nil, err
	}
	for _, level := range topKeys(levels, len(levels)) {
		response.Levels = append(response.Levels, logs_core.VolumeCountDTO{Name: level, Count: levels[level]})
	}
	todayCount, err := c.by(nil, "", today, now.Add(-time.Nanosecond))
	if err != nil {
		return nil, err
	}
	yesterdayCount, err := c.by(nil, "", today.Add(-day), now.Add(-day-time.Nanosecond))
	if err != nil {
		return nil, err
	}
	response.Today, response.YesterdaySoFar = todayCount[""], yesterdayCount[""]

	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read log stats: %w", err)
	}
	if stats != nil && stats.TotalLogs > 0 {
		response.StoredBytes = int64(stats.TotalSizeMB * 1024 * 1024)
		response.BytesPerLine = stats.TotalSizeMB * 1024 * 1024 / float64(stats.TotalLogs)
		response.RawBytesPerLine = stats.RawSizeMB * 1024 * 1024 / float64(stats.TotalLogs)
	}

	noisy, err := c.by(
		nil,
		logs_core.JoinGroupFields(logs_core.PatternHashField, "service_name", "level"),
		weekAgo,
		now,
	)
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
	for _, key := range topKeys(noisy, len(noisy)) {
		parts := logs_core.SplitGroupKey(key, 3)
		if parts[0] == "" {
			continue
		}
		response.Noisy = append(response.Noisy, logs_core.NoisyPatternDTO{
			Hash: parts[0], Service: parts[1], Level: parts[2], Count: noisy[key],
			PerDay: float64(noisy[key]) / float64(activeDays),
		})
		if !seen[parts[0]] {
			seen[parts[0]] = true
			hashes = append(hashes, parts[0])
		}
		if len(response.Noisy) == volumeNoisyPatterns {
			break
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
		fields = []string{"k8s_namespace_name", "k8s_pod_name", "service_name"}
		place = func(values []string) (string, string) {
			tile := unknown(values[2], "(unknown)")
			if values[1] != "" {
				tile = WorkloadOfPod(values[1])
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

	c := s.newCounter(ctx, projectID)
	current, err := c.by(request.Query, logs_core.JoinGroupFields(append(fields, "level")...), from, end)
	if err != nil {
		return nil, err
	}
	previous, err := c.by(request.Query, logs_core.JoinGroupFields(fields...), from.Add(-day), end.Add(-day))
	if err != nil {
		return nil, err
	}

	type tileKey struct{ group, tile string }
	tiles := map[tileKey]*logs_core.HealthTileDTO{}
	tileOf := func(group, tile string) *logs_core.HealthTileDTO {
		key := tileKey{group, tile}
		if tiles[key] == nil {
			tiles[key] = &logs_core.HealthTileDTO{Name: tile}
		}
		return tiles[key]
	}
	for key, count := range current {
		values := logs_core.SplitGroupKey(key, len(fields)+1)
		group, name := place(values)
		tile := tileOf(group, name)
		tile.Total += count
		level := strings.ToLower(values[len(fields)])
		if errorLevels[level] {
			tile.Errors += count
		} else if warnLevels[level] {
			tile.Warnings += count
		}
	}
	for key, count := range previous {
		group, name := place(logs_core.SplitGroupKey(key, len(fields)))
		if tile := tiles[tileKey{group, name}]; tile != nil {
			tile.Previous += count
		}
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

// by is the lines matching filter in [from, to] per group key: one bucket
// as wide as the range in whole minutes, so per-minute counts answer it.
func (c *counter) by(
	filter *logs_core.QueryNode, groupField string, from, to time.Time,
) (map[string]int64, error) {
	width := to.Sub(from).Truncate(time.Minute) + time.Minute
	series, err := c.series(filter, groupField, from, to, width)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for key, buckets := range series {
		for _, count := range buckets {
			counts[key] += count
		}
	}
	return counts, nil
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

// explainMarginals is each (field, value)'s lines, from counts keyed by
// explainDimensions' values, plus the total; pods add their ReplicaSet.
func explainMarginals(counts map[string]int64) (map[[2]string]int64, int64) {
	values := map[[2]string]int64{}
	var total int64
	for key, count := range counts {
		parts := logs_core.SplitGroupKey(key, len(explainDimensions))
		total += count
		for i, field := range explainDimensions {
			if parts[i] != "" {
				values[[2]string{field, parts[i]}] += count
			}
		}
		if replicaSet := ReplicaSetOfPod(parts[3]); replicaSet != "" {
			values[[2]string{replicaSetField, replicaSet}] += count
		}
	}
	return values, total
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

func unionKeys(maps ...map[string]int64) []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, m := range maps {
		for key := range m {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
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
