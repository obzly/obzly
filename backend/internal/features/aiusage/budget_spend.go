package aiusage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"obzly/internal/cache"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

// Spend sources a budget counts.
const (
	sourceClaudeCode = "claude_code"
	sourceLLMApps    = "llm"
)

const (
	// dayCloseLag: a day is closed (its spend cached for good) this long
	// after it ends, so normal ingest lag still lands in it.
	dayCloseLag      = time.Hour
	dayCacheTTL      = 40 * 24 * time.Hour
	dayCachePrefix   = "ai-budget-spend:v1:"
	spendCacheTimout = 2 * time.Second
)

// dayRow is one day's total for a user and model: Claude Code's cost, or
// an LLM app's tokens (priced when read, so a price change applies to past
// days too).
type dayRow struct {
	User   string  `json:"u,omitempty"`
	Model  string  `json:"m,omitempty"`
	Cost   float64 `json:"c,omitempty"`
	Input  float64 `json:"i,omitempty"`
	Output float64 `json:"o,omitempty"`
	Cached float64 `json:"k,omitempty"`
}

// spendCache keeps closed days' rows; a nil entry from get is a miss.
type spendCache interface {
	get(ctx context.Context, keys []string) ([][]byte, error)
	set(ctx context.Context, entries map[string][]byte, ttl time.Duration) error
}

// spendSource reads a project's rows per day: days are the starts of
// consecutive days, the last one running to `to`.
type spendSource interface {
	dayRows(
		ctx context.Context, source string, projectID uuid.UUID, days []time.Time, to time.Time, scanBudget int64,
	) ([][]dayRow, error)
}

type valkeySpendCache struct{}

type storedSpend struct{}

// periodSpend is the budget's spend from periodStart to now: closed days
// from the cache (filled on a miss), the rest read live. A day's entry holds
// every user and model, so all budgets of a project, source and time zone
// share it whatever their scope.
func (s *AIUsageService) periodSpend(
	ctx context.Context, budget *Budget, prices []*ModelPrice, periodStart, now time.Time,
) (*budgetSpend, error) {
	spend := &budgetSpend{byKey: map[string]float64{}, byModel: map[string]float64{}}
	days := dayStarts(periodStart, now, budget.location())
	sources := []string{}
	if budget.IncludeClaudeCode {
		sources = append(sources, sourceClaudeCode)
	}
	if budget.IncludeLLMApps && len(prices) > 0 {
		sources = append(sources, sourceLLMApps)
	}
	for _, source := range sources {
		byDay, err := s.sourceDays(ctx, source, budget, days, now)
		if err != nil {
			return nil, err
		}
		for _, rows := range byDay {
			for _, row := range rows {
				key, ok := budget.groupOf(row)
				if !ok {
					continue
				}
				cost := row.Cost
				if source == sourceLLMApps {
					price := matchPrice(prices, row.Model)
					if price == nil {
						continue
					}
					cost = price.cost(row.Input, row.Output, row.Cached)
				}
				if cost <= 0 {
					continue
				}
				spend.byKey[key] += cost
				spend.byModel[row.Model] += cost
			}
		}
	}
	return spend, nil
}

// sourceDays is one source's rows for each of days. Closed days come from
// the cache; everything from the first closed day not cached is read in one
// query, and the closed days it read are cached.
func (s *AIUsageService) sourceDays(
	ctx context.Context, source string, budget *Budget, days []time.Time, now time.Time,
) ([][]dayRow, error) {
	closed := 0
	for closed < len(days)-1 && !now.Before(days[closed+1].Add(dayCloseLag)) {
		closed++
	}
	keys := make([]string, closed)
	for i := range closed {
		keys[i] = dayCacheKey(budget.ProjectID, source, budget.TimeZone, days[i], budget.location())
	}

	result := make([][]dayRow, len(days))
	first := 0
	if closed > 0 {
		cached, err := s.spendCache.get(ctx, keys)
		if err != nil {
			logger.GetLogger().Warn("AI budget spend cache unavailable, reading every day", "error", err)
			cached = make([][]byte, closed)
		}
		for first < closed && cached[first] != nil && json.Unmarshal(cached[first], &result[first]) == nil {
			first++
		}
	}

	read, err := s.spendSource.dayRows(ctx, source, budget.ProjectID, days[first:], now, s.scanBudget())
	if err != nil {
		return nil, err
	}
	entries := map[string][]byte{}
	for i, rows := range read {
		result[first+i] = rows
		if first+i < closed {
			if rows == nil {
				rows = []dayRow{}
			}
			if encoded, err := json.Marshal(rows); err == nil {
				entries[keys[first+i]] = encoded
			}
		}
	}
	if len(entries) > 0 {
		if err := s.spendCache.set(ctx, entries, dayCacheTTL); err != nil {
			logger.GetLogger().Warn("AI budget spend cache not updated", "error", err)
		}
	}
	return result, nil
}

// groupOf is the budget's group for a row, or false when the row doesn't
// count: per-user and per-model budgets skip rows without a user or model.
func (b *Budget) groupOf(row dayRow) (string, bool) {
	switch b.Scope {
	case ScopeEachUser:
		return row.User, row.User != ""
	case ScopeEachModel:
		return row.Model, row.Model != ""
	case ScopeUser:
		return "", row.User == b.ScopeValue
	case ScopeModel:
		return "", row.Model == b.ScopeValue
	}
	return "", true
}

func (valkeySpendCache) get(ctx context.Context, keys []string) ([][]byte, error) {
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, spendCacheTimout)
	defer cancel()
	values, err := client.Do(ctx, client.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, err
	}
	result := make([][]byte, len(keys))
	for i, value := range values {
		if i >= len(keys) {
			break
		}
		text, err := value.ToString()
		if valkey.IsValkeyNil(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[i] = []byte(text)
	}
	return result, nil
}

func (valkeySpendCache) set(ctx context.Context, entries map[string][]byte, ttl time.Duration) error {
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, spendCacheTimout)
	defer cancel()
	commands := make(valkey.Commands, 0, len(entries))
	for key, value := range entries {
		commands = append(commands, client.B().Set().Key(key).Value(string(value)).Ex(ttl).Build())
	}
	for _, result := range client.DoMulti(ctx, commands...) {
		if err := result.Error(); err != nil {
			return err
		}
	}
	return nil
}

func (storedSpend) dayRows(
	ctx context.Context, source string, projectID uuid.UUID, days []time.Time, to time.Time, scanBudget int64,
) ([][]dayRow, error) {
	result := make([][]dayRow, len(days))
	if len(days) == 0 {
		return result, nil
	}
	if source == sourceLLMApps {
		statement := `SELECT ` + dayIndexSQL("epoch_ms(ts::TIMESTAMP)", days) + `, user_id, model,
	coalesce(sum(input_tokens), 0)::DOUBLE, coalesce(sum(output_tokens), 0)::DOUBLE,
	coalesce(sum(cached_tokens), 0)::DOUBLE
FROM llm GROUP BY 1, 2, 3`
		rows, err := queryLLM(ctx, projectID, days[0], to, scanBudget, statement)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			day := int(toInt(r[0]))
			if day < 0 || day >= len(result) {
				continue
			}
			result[day] = append(result[day], dayRow{User: toString(r[1]), Model: toString(r[2]),
				Input: toFloat(r[3]), Output: toFloat(r[4]), Cached: toFloat(r[5])})
		}
		return result, nil
	}

	statement := `WITH e AS (SELECT timestamp AS ts, message AS ev, fields::JSON AS f FROM logs)
SELECT ` + dayIndexSQL("epoch_ms(ts::TIMESTAMP)", days) + `, coalesce(` + str("user_email") + `, ''),
	coalesce(` + str("model") + `, ''), (coalesce(sum(` + num("cost_usd_micros") + `), 0) / 1e6)::DOUBLE
FROM e WHERE ev = ` + quote(eventAPIRequest) + `
GROUP BY 1, 2, 3`
	results, err := logs_core.GetAIRepository().ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From: days[0], To: to, MessagePrefix: eventAPIRequest, ScanBudget: scanBudget,
		Statements: []logs_core.ScopedSQLStatement{{SQL: statement}},
	})
	if err != nil {
		return nil, err
	}
	for _, r := range results[0].Rows {
		day := int(toInt(r[0]))
		if day < 0 || day >= len(result) {
			continue
		}
		result[day] = append(result[day], dayRow{User: toString(r[1]), Model: toString(r[2]), Cost: toFloat(r[3])})
	}
	return result, nil
}

// dayStarts are the starts of the days in loc from periodStart through the
// day holding now.
func dayStarts(periodStart, now time.Time, loc *time.Location) []time.Time {
	local := periodStart.In(loc)
	year, month, day := local.Date()
	days := []time.Time{}
	for i := 0; ; i++ {
		start := time.Date(year, month, day+i, 0, 0, 0, 0, loc).UTC()
		if i > 0 && start.After(now) {
			return days
		}
		days = append(days, start)
	}
}

// dayIndexSQL numbers each row's day among days (0 = days[0]). The bounds
// are integers computed here, never user input.
func dayIndexSQL(epochMs string, days []time.Time) string {
	if len(days) == 1 {
		return "0"
	}
	var b strings.Builder
	b.WriteString("CASE")
	for i, start := range days[1:] {
		b.WriteString(" WHEN " + epochMs + " < " + strconv.FormatInt(start.UnixMilli(), 10) +
			" THEN " + strconv.Itoa(i))
	}
	b.WriteString(" ELSE " + strconv.Itoa(len(days)-1) + " END")
	return b.String()
}

func dayCacheKey(projectID uuid.UUID, source, timeZone string, day time.Time, loc *time.Location) string {
	return fmt.Sprintf("%s%s:%s:%s:%s", dayCachePrefix, projectID, source, timeZone, day.In(loc).Format("2006-01-02"))
}
