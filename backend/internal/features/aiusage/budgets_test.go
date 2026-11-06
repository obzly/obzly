package aiusage

import (
	"testing"
	"time"

	"obzly/internal/features/alerting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_PeriodBounds_InNonUTCZone_AlignsToLocalCalendar(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	require.NoError(t, err)
	// 2026-10-25 20:00 UTC is Monday 2026-10-26 01:30 in Kolkata.
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)

	start, end := periodBounds(PeriodDay, kolkata, now)
	assert.Equal(t, time.Date(2026, 10, 25, 18, 30, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 10, 26, 18, 30, 0, 0, time.UTC), end)

	start, end = periodBounds(PeriodWeek, kolkata, now)
	assert.Equal(t, time.Date(2026, 10, 25, 18, 30, 0, 0, time.UTC), start, "weeks start on Monday")
	assert.Equal(t, time.Date(2026, 11, 1, 18, 30, 0, 0, time.UTC), end)

	start, end = periodBounds(PeriodMonth, kolkata, now)
	assert.Equal(t, time.Date(2026, 9, 30, 18, 30, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 10, 31, 18, 30, 0, 0, time.UTC), end)
}

func Test_PeriodBounds_AcrossDSTChange_DayIsLocalMidnightToMidnight(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	// Berlin leaves summer time on 2026-10-25: that day has 25 hours.
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	start, end := periodBounds(PeriodDay, berlin, now)
	assert.Equal(t, time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC), end)
	assert.Equal(t, 25*time.Hour, end.Sub(start))

	// Sunday belongs to the week that started on Monday the 19th.
	start, _ = periodBounds(PeriodWeek, berlin, now)
	assert.Equal(t, time.Date(2026, 10, 18, 22, 0, 0, 0, time.UTC), start)
}

func Test_Forecast_ExtrapolatesAtCurrentRate(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	assert.InDelta(t, 3100, forecast(1000, start, end, start.Add(10*24*time.Hour)), 1e-6)
	assert.InDelta(t, 1000, forecast(1000, start, end, end), 1e-6, "a finished period forecasts what was spent")
	assert.InDelta(t, 10*744, forecast(10, start, end, start.Add(time.Minute)), 1e-6,
		"the first hour is extrapolated as a whole hour")
}

func Test_BudgetSamples_EachUser_OneSeriesPerCrossedThresholdWithSeverity(t *testing.T) {
	budget := &Budget{AmountUSD: 40, Scope: ScopeEachUser, Thresholds: Thresholds{80, 100}, TimeZone: "UTC"}
	spend := &budgetSpend{byKey: map[string]float64{
		"a.rivera@example.com": 46, "j.chen@example.com": 34, "s.patel@example.com": 20,
	}}

	samples := budgetSamples(budget, spend, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))

	require.Len(t, samples, 3)
	byKey := map[string]alerting.SourceSample{}
	for _, s := range samples {
		byKey[s.Labels["user"]+"/"+s.Labels["threshold"]] = s
		assert.Equal(t, "2026-10-27", s.Labels["period"])
	}
	assert.Equal(t, "critical", byKey["a.rivera@example.com/100"].Labels["severity"])
	assert.Equal(t, "warning", byKey["a.rivera@example.com/80"].Labels["severity"])
	assert.InDelta(t, 115, byKey["a.rivera@example.com/100"].Value, 1e-9)
	assert.Equal(t, "warning", byKey["j.chen@example.com/80"].Labels["severity"])
}

func Test_FillStatus_EachModel_ReportsTopAndGroupCounts(t *testing.T) {
	budget := &Budget{AmountUSD: 100, Scope: ScopeEachModel, Thresholds: Thresholds{80, 100}}
	spend := &budgetSpend{byKey: map[string]float64{"opus": 120, "sonnet": 85, "haiku": 10}}
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	status := BudgetStatusDTO{}

	fillStatus(&status, budget, spend, start, start.Add(24*time.Hour), start.Add(12*time.Hour))

	assert.InDelta(t, 215, status.SpentUSD, 1e-9)
	assert.InDelta(t, 120, status.Percent, 1e-9, "a per-model budget shows its highest model")
	assert.InDelta(t, 240, status.ForecastUSD, 1e-9)
	assert.Equal(t, 3, status.Groups)
	assert.Equal(t, 1, status.GroupsWarning)
	assert.Equal(t, 1, status.GroupsOver)
	require.Len(t, status.Top, 3)
	assert.Equal(t, "opus", status.Top[0].Key)
}

func Test_ApplyBudget_RejectsInvalidRequests(t *testing.T) {
	valid := func() BudgetRequest {
		return BudgetRequest{Name: "Monthly team budget", AmountUSD: 100, Period: PeriodMonth, Scope: ScopeProject}
	}
	budget := &Budget{}
	request := valid()
	require.NoError(t, applyBudget(budget, request))
	assert.Equal(t, "UTC", budget.TimeZone)
	assert.Equal(t, Thresholds{80, 100}, budget.Thresholds)
	assert.True(t, budget.IncludeClaudeCode)

	for name, mutate := range map[string]func(*BudgetRequest){
		"zero amount":       func(r *BudgetRequest) { r.AmountUSD = 0 },
		"unknown period":    func(r *BudgetRequest) { r.Period = "year" },
		"unknown zone":      func(r *BudgetRequest) { r.TimeZone = "Mars/Olympus" },
		"one user, no name": func(r *BudgetRequest) { r.Scope = ScopeUser },
		"counts nothing": func(r *BudgetRequest) {
			off := false
			r.IncludeClaudeCode = &off
		},
		"threshold out of range": func(r *BudgetRequest) { r.Thresholds = []float64{0} },
	} {
		request := valid()
		mutate(&request)
		assert.ErrorIs(t, applyBudget(&Budget{}, request), ErrInvalidRequest, name)
	}
}
