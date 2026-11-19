package logs_querying

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
)

const (
	patternChangeCountKeyPrefix = "logs:pattern-change-count:"
	// A window still filling (ending in the last few minutes) is recounted after a minute;
	// an older one hardly changes.
	patternChangeCountLiveTTL = time.Minute
	patternChangeCountTTL     = 10 * time.Minute
	patternChangeCountLiveFor = 5 * time.Minute
)

// countPatternChanges answers a CountOnly request over whole minutes (all from rollups
// where segments have them: whole hours hourly, the rest per minute), cached per
// project, query and ranges.
func (s *LogQueryService) countPatternChanges(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.PatternChangesRequestDTO,
) (*logs_core.PatternChangesResponseDTO, error) {
	request.Current, request.Baseline = wholeMinutes(request.Current), wholeMinutes(request.Baseline)
	key := patternChangeCountKey(projectID, request)
	if s.cache != nil {
		cached, err := s.cache.Do(ctx, s.cache.B().Get().Key(key).Build()).AsBytes()
		if err == nil {
			var response logs_core.PatternChangesResponseDTO
			if json.Unmarshal(cached, &response) == nil {
				return &response, nil
			}
		}
	}
	response, err := logs_core.GetLogCoreRepository().ExecutePatternChanges(ctx, projectID, request)
	if err != nil || s.cache == nil {
		return response, err
	}
	ttl := patternChangeCountTTL
	if request.Current.To.After(time.Now().UTC().Add(-patternChangeCountLiveFor)) {
		ttl = patternChangeCountLiveTTL
	}
	if encoded, err := json.Marshal(response); err == nil {
		if err := s.cache.Do(ctx, s.cache.B().Set().Key(key).Value(string(encoded)).
			Ex(ttl).Build()).Error(); err != nil {
			s.logger.Warn("failed to cache pattern change count", slog.String("error", err.Error()))
		}
	}
	return response, nil
}

// wholeMinutes cuts a range to whole minutes ([from's minute, to's minute)), so counts
// come from per-minute rollups alone and requests within the same minute share a cache entry.
func wholeMinutes(timeRange *logs_core.TimeRangeDTO) *logs_core.TimeRangeDTO {
	cut := &logs_core.TimeRangeDTO{}
	if timeRange.From != nil {
		from := timeRange.From.UTC().Truncate(time.Minute)
		cut.From = &from
	}
	to := timeRange.To.UTC().Truncate(time.Minute).Add(-time.Nanosecond)
	if cut.From != nil && to.Before(*cut.From) {
		to = *cut.From
	}
	cut.To = &to
	return cut
}

func patternChangeCountKey(projectID uuid.UUID, request *logs_core.PatternChangesRequestDTO) string {
	query, _ := json.Marshal(request.Query)
	hash := sha256.New()
	hash.Write(query)
	for _, timeRange := range []*logs_core.TimeRangeDTO{request.Current, request.Baseline} {
		from := int64(0)
		if timeRange.From != nil {
			from = timeRange.From.UnixNano()
		}
		hash.Write([]byte("|" + strconv.FormatInt(from, 10) + "|" + strconv.FormatInt(timeRange.To.UnixNano(), 10)))
	}
	hash.Write([]byte("|" + strconv.FormatInt(request.MinCount, 10)))
	return patternChangeCountKeyPrefix + projectID.String() + ":" + hex.EncodeToString(hash.Sum(nil))
}
