package aiusage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryCache struct {
	entries map[string][]byte
	fail    bool
}

type spyRead struct {
	source     string
	from       time.Time
	days       int
	scanBudget int64
}

// spySource has one Claude Code row ($1, a.rivera, claude-opus-4-1) and one
// LLM app row (1M input tokens of gpt-4.1-mini) every day.
type spySource struct {
	reads []spyRead
}

func Test_PeriodSpend_SecondEvaluation_ReadsOnlyTheOpenDay(t *testing.T) {
	cache, source := &memoryCache{entries: map[string][]byte{}}, &spySource{}
	service := testService(cache, source)
	projectID := uuid.New()
	monthly := &Budget{ProjectID: projectID, Scope: ScopeProject, TimeZone: "UTC", IncludeClaudeCode: true}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	spend, err := service.periodSpend(context.Background(), monthly, nil, start, now)
	require.NoError(t, err)
	assert.InDelta(t, 27, spend.byKey[""], 1e-9)
	require.Len(t, source.reads, 1)
	assert.Equal(t, start, source.reads[0].from)
	assert.Equal(t, 27, source.reads[0].days)
	assert.Len(t, cache.entries, 26, "every closed day is cached")

	spend, err = service.periodSpend(context.Background(), monthly, nil, start, now.Add(5*time.Minute))
	require.NoError(t, err)
	assert.InDelta(t, 27, spend.byKey[""], 1e-9)
	require.Len(t, source.reads, 2)
	assert.Equal(t, now.Truncate(24*time.Hour), source.reads[1].from, "only today is read again")
	assert.Equal(t, 1, source.reads[1].days)

	// Another budget of the project and zone shares the days, whatever its scope.
	perUser := &Budget{ProjectID: projectID, Scope: ScopeEachUser, TimeZone: "UTC", IncludeClaudeCode: true}
	spend, err = service.periodSpend(context.Background(), perUser, nil, start, now)
	require.NoError(t, err)
	assert.InDelta(t, 27, spend.byKey["a.rivera@example.com"], 1e-9)
	assert.Equal(t, 1, source.reads[2].days)
}

func Test_PeriodSpend_DayIsCachedOnlyAnHourAfterItEnds(t *testing.T) {
	cache, source := &memoryCache{entries: map[string][]byte{}}, &spySource{}
	service := testService(cache, source)
	weekly := &Budget{ProjectID: uuid.New(), Scope: ScopeProject, TimeZone: "UTC", IncludeClaudeCode: true}
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

	_, err := service.periodSpend(context.Background(), weekly, nil, monday, monday.Add(24*time.Hour+59*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, cache.entries, "Monday is still open for late data at 00:59 on Tuesday")

	_, err = service.periodSpend(context.Background(), weekly, nil, monday, monday.Add(25*time.Hour))
	require.NoError(t, err)
	assert.Contains(t, cache.entries, dayCacheKey(weekly.ProjectID, sourceClaudeCode, "UTC", monday, time.UTC))
	assert.Len(t, cache.entries, 1)
}

func Test_PeriodSpend_InNonUTCZone_KeysDaysByLocalDate(t *testing.T) {
	cache, source := &memoryCache{entries: map[string][]byte{}}, &spySource{}
	service := testService(cache, source)
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	require.NoError(t, err)
	budget := &Budget{ProjectID: uuid.New(), Scope: ScopeProject, TimeZone: "Asia/Kolkata", IncludeClaudeCode: true}
	start, _ := periodBounds(PeriodWeek, kolkata, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))

	_, err = service.periodSpend(context.Background(), budget, nil, start, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Contains(t, cache.entries, "ai-budget-spend:v1:"+budget.ProjectID.String()+":claude_code:Asia/Kolkata:2026-10-26")
	assert.Equal(t, time.Date(2026, 10, 25, 18, 30, 0, 0, time.UTC), source.reads[0].from)
}

func Test_PeriodSpend_WhenCacheFails_StillReadsEveryDayCorrectly(t *testing.T) {
	source := &spySource{}
	service := testService(&memoryCache{fail: true}, source)
	budget := &Budget{ProjectID: uuid.New(), Scope: ScopeEachModel, TimeZone: "UTC", IncludeClaudeCode: true}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	for range 2 {
		spend, err := service.periodSpend(context.Background(), budget, nil, start, now)
		require.NoError(t, err)
		assert.InDelta(t, 10, spend.byKey["claude-opus-4-1"], 1e-9)
	}
	require.Len(t, source.reads, 2)
	assert.Equal(t, 10, source.reads[1].days, "nothing cached, so every day is read again")
}

func Test_PeriodSpend_LLMApps_PricesCachedTokensAndPassesTheScanLimit(t *testing.T) {
	cache, source := &memoryCache{entries: map[string][]byte{}}, &spySource{}
	service := testService(cache, source)
	budget := &Budget{ProjectID: uuid.New(), Scope: ScopeProject, TimeZone: "UTC", IncludeLLMApps: true}
	prices := []*ModelPrice{{Model: "gpt-4.1-mini*", InputPerMillion: 0.4, OutputPerMillion: 1.6}}
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

	spend, err := service.periodSpend(context.Background(), budget, prices, start, start.Add(49*time.Hour))
	require.NoError(t, err)
	assert.InDelta(t, 3*0.4, spend.byKey[""], 1e-9)
	assert.Equal(t, int64(5<<30), source.reads[0].scanBudget)
	assert.Equal(t, sourceLLMApps, source.reads[0].source)

	prices[0].InputPerMillion = 1
	spend, err = service.periodSpend(context.Background(), budget, prices, start, start.Add(49*time.Hour))
	require.NoError(t, err)
	assert.InDelta(t, 3*1.0, spend.byKey[""], 1e-9, "cached days hold tokens, so a new price applies to them")
}

func Test_ValkeySpendCache_RoundTripsAndReportsMisses(t *testing.T) {
	key := dayCachePrefix + "test:" + uuid.NewString()
	missing := dayCachePrefix + "test:" + uuid.NewString()
	store := valkeySpendCache{}

	require.NoError(t, store.set(context.Background(), map[string][]byte{key: []byte(`[{"u":"a"}]`)}, time.Minute))
	values, err := store.get(context.Background(), []string{key, missing})

	require.NoError(t, err)
	assert.Equal(t, `[{"u":"a"}]`, string(values[0]))
	assert.Nil(t, values[1])
}

func Test_DayIndexSQL_NumbersDaysByTheirBounds(t *testing.T) {
	days := []time.Time{time.UnixMilli(1000), time.UnixMilli(2000), time.UnixMilli(3000)}
	assert.Equal(t, "CASE WHEN ms < 2000 THEN 0 WHEN ms < 3000 THEN 1 ELSE 2 END", dayIndexSQL("ms", days))
	assert.Equal(t, "0", dayIndexSQL("ms", days[:1]))
}

func (c *memoryCache) get(_ context.Context, keys []string) ([][]byte, error) {
	if c.fail {
		return nil, errors.New("valkey is down")
	}
	result := make([][]byte, len(keys))
	for i, key := range keys {
		result[i] = c.entries[key]
	}
	return result, nil
}

func (c *memoryCache) set(_ context.Context, entries map[string][]byte, _ time.Duration) error {
	if c.fail {
		return errors.New("valkey is down")
	}
	for key, value := range entries {
		c.entries[key] = value
	}
	return nil
}

func (s *spySource) dayRows(
	_ context.Context, source string, _ uuid.UUID, days []time.Time, _ time.Time, scanBudget int64,
) ([][]dayRow, error) {
	s.reads = append(s.reads, spyRead{source: source, from: days[0], days: len(days), scanBudget: scanBudget})
	result := make([][]dayRow, len(days))
	for i := range days {
		if source == sourceLLMApps {
			result[i] = []dayRow{{Model: "gpt-4.1-mini-2025-04-14", Input: 1_000_000}, {Model: "unpriced", Input: 5}}
		} else {
			result[i] = []dayRow{{User: "a.rivera@example.com", Model: "claude-opus-4-1", Cost: 1}}
		}
	}
	return result, nil
}

func testService(cache spendCache, source spendSource) *AIUsageService {
	return &AIUsageService{nil, nil, cache, source, func() int64 { return 5 << 30 }}
}
