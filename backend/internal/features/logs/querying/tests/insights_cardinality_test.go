package logs_querying_tests

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_querying "obzly/internal/features/logs/querying"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Patterns tab over hundreds of patterns: the counts of new, spiking and
// dropping patterns come from the merge (only the listed patterns come back),
// and a pattern seen 3 days ago isn't new. Volume & noise over nine services
// keeps the six biggest and sums the rest as other.
func Test_PatternTrends_OverManyPatterns_CountsEveryChangeExactly(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Pattern trends cardinality")
	unlimited(project, owner.Token, router)
	now := time.Now().UTC().Truncate(10 * time.Minute)
	from := now.Add(-time.Hour)
	logs := &insightLogs{uniqueID: uniqueID}
	kinds := []struct {
		name                   string
		patterns               int
		now, yesterday, before int
	}{
		{"steady", 200, 3, 3, 0},
		{"fresh", 300, 5, 0, 0},
		{"spiky", 100, 10, 2, 0},
		{"fading", 100, 1, 6, 0},
		{"returning", 100, 5, 0, 2},
	}
	services := map[string]int64{}
	for _, kind := range kinds {
		for i := range kind.patterns {
			message := "event " + kind.name + " " + letters(i) + " happened"
			service := "svc-" + letters(i%9)
			logs.addMessages(from, kind.now, message, service)
			logs.addMessages(from.Add(-24*time.Hour), kind.yesterday, message, service)
			logs.addMessages(from.Add(-3*24*time.Hour), kind.before, message, service)
			services[service] += int64(kind.now)
		}
	}
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.PatternTrendsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("pattern-trends", project.ID), "Bearer "+owner.Token,
		logs_core.PatternTrendsRequestDTO{
			Query:     BuildCondition("test_id", "equals", uniqueID),
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
			Limit:     30,
		}, http.StatusOK, &response)

	assert.Equal(t, int64(200*3+300*5+100*10+100*1+100*5), response.Total)
	assert.Equal(t, 800, response.Distinct)
	assert.Equal(t, 300, response.NewCount, "fresh only: returning patterns had lines 3 days ago")
	assert.Equal(t, 100, response.SpikeCount)
	assert.Equal(t, 100, response.DropCount)
	assert.False(t, response.Partial)
	assert.LessOrEqual(t, len(response.Patterns), 4*30)
	for _, pattern := range response.Patterns {
		kind := strings.Fields(pattern.Sample)[1]
		switch kind {
		case "fresh":
			assert.Equal(t, logs_core.PatternChangeNew, pattern.Change, pattern.Sample)
		case "spiky":
			assert.Equal(t, logs_core.PatternChangeSpike, pattern.Change, pattern.Sample)
		case "fading":
			assert.Equal(t, logs_core.PatternChangeDrop, pattern.Change, pattern.Sample)
		case "returning":
			assert.Equal(t, "", pattern.Change, pattern.Sample)
			assert.Equal(t, int64(2), pattern.PreviousWeek, pattern.Sample)
		}
	}

	var volume logs_core.VolumeResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("volume", project.ID), "Bearer "+owner.Token,
		logs_core.VolumeRequestDTO{Days: 1}, http.StatusOK, &volume)
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if services[names[i]] != services[names[j]] {
			return services[names[i]] > services[names[j]]
		}
		return names[i] < names[j]
	})
	if from.Truncate(24*time.Hour) == now.Truncate(24*time.Hour) {
		require.Len(t, volume.Services, 7, "six services and other")
		var other int64
		for _, name := range names[6:] {
			other += services[name]
		}
		for i, name := range names[:6] {
			assert.Equal(t, name, volume.Services[i].Name)
			assert.Equal(t, services[name], volume.Services[i].Total, name)
		}
		assert.True(t, volume.Services[6].Other)
		assert.Equal(t, other, volume.Services[6].Total)
	}
}

// Health map over hundreds of pods of every kind of workload: pods become
// workloads in SQL exactly as WorkloadOfPod names them.
func Test_HealthMap_OverManyPods_GroupsThemIntoTheirWorkloads(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Health map cardinality")
	unlimited(project, owner.Token, router)
	now := time.Now().UTC().Truncate(time.Minute)
	from := now.Add(-time.Hour)
	logs := &insightLogs{uniqueID: uniqueID}
	type tile struct{ total, errors, warnings, previous int64 }
	want := map[string]map[string]*tile{}
	add := func(namespace, pod, service string, level logs_core.LogLevel, n int, at time.Time, previous bool) {
		fields := map[string]any{"service_name": service}
		group, name := "(outside Kubernetes)", service
		if namespace != "" {
			fields["k8s_namespace_name"] = namespace
			group = namespace
		}
		if pod != "" {
			fields["k8s_pod_name"] = pod
			name = logs_querying.WorkloadOfPod(pod)
		}
		logs.add(at, n, level, "work %d done", fields)
		if want[group] == nil {
			want[group] = map[string]*tile{}
		}
		if want[group][name] == nil {
			want[group][name] = &tile{}
		}
		entry := want[group][name]
		if previous {
			entry.previous += int64(n)
			return
		}
		entry.total += int64(n)
		switch level {
		case logs_core.LogLevelError:
			entry.errors += int64(n)
		case logs_core.LogLevelWarn:
			entry.warnings += int64(n)
		}
	}
	for i := range 300 {
		add("shop", "checkout-7d4b9c8f6-"+generatedSuffix(i), "checkout", logs_core.LogLevelInfo, 2, from, false)
		add("shop", "checkout-6c5b8d9f7-"+generatedSuffix(i), "checkout", logs_core.LogLevelError, 1, from, false)
	}
	for i := range 150 {
		add("logging", "fluent-bit-"+generatedSuffix(i), "fluent-bit", logs_core.LogLevelWarn, 1, from, false)
	}
	for i := range 20 {
		add("data", "postgres-"+strconv.Itoa(i), "postgres", logs_core.LogLevelInfo, 1, from, false)
		add("data", "search-indexer", "search", logs_core.LogLevelInfo, 1, from, false)
	}
	add("", "", "batch", logs_core.LogLevelError, 4, from, false)
	add("shop", "checkout-7d4b9c8f6-"+generatedSuffix(0), "checkout", logs_core.LogLevelInfo, 7, from.Add(-24*time.Hour), true)
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.HealthMapResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("health-map", project.ID), "Bearer "+owner.Token,
		logs_core.HealthMapRequestDTO{
			Query:     BuildCondition("test_id", "equals", uniqueID),
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
			GroupBy:   logs_core.HealthMapByKubernetes,
		}, http.StatusOK, &response)

	got := map[string]map[string]*tile{}
	for _, group := range response.Groups {
		got[group.Name] = map[string]*tile{}
		for _, t := range group.Tiles {
			got[group.Name][t.Name] = &tile{t.Total, t.Errors, t.Warnings, t.Previous}
		}
	}
	require.Equal(t, want, got)
	assert.Equal(t, 0, response.HiddenTiles)
	assert.False(t, response.Partial)
}

// unlimited turns the project's rate limit off: these tests send thousands
// of logs at once.
func unlimited(project *projects_models.Project, token string, router *gin.Engine) {
	projects_testing.UpdateProject(project, &projects_models.Project{Name: project.Name}, token, router)
}

// addMessages appends n logs of message one minute apart from from+1m.
func (l *insightLogs) addMessages(from time.Time, n int, message, service string) {
	for i := range n {
		at := from.Add(time.Duration(i%25+1) * time.Minute)
		l.items = append(l.items, logs_receiving.LogItemRequestDTO{
			Level: logs_core.LogLevelInfo, Message: message, Timestamp: at.Format(time.RFC3339Nano),
			Fields: map[string]any{"test_id": l.uniqueID, "service_name": service},
		})
		if time.Since(at) < 2*time.Hour {
			l.recent++
		}
	}
}

// letters writes i in the letters g-z: no digits or hex runs, so each value
// is its own message pattern.
func letters(i int) string {
	const alphabet = "ghijklmnopqrstuvwxyz"
	word := string(alphabet[i%len(alphabet)])
	for i /= len(alphabet); i > 0; i /= len(alphabet) {
		word = string(alphabet[i%len(alphabet)]) + word
	}
	return word
}

// generatedSuffix is a 5-character Kubernetes-style pod suffix for i.
func generatedSuffix(i int) string {
	const alphabet = "bcdfghjklmnpqrstvwxz2456789"
	suffix := make([]byte, 5)
	for j := range suffix {
		suffix[j] = alphabet[i%len(alphabet)]
		i /= len(alphabet)
	}
	return string(suffix)
}

// Within a week of a pattern version change, a pattern the new version
// regrouped (it had one hash per name before) isn't new; a pattern that
// really appeared still is.
func Test_PatternTrends_WhenTheWeekReachesBeforeThePatternVersion_LeavesRegroupedPatternsOutOfNew(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Pattern trends version")
	unlimited(project, owner.Token, router)
	since := time.Now().UTC().Add(-2 * time.Hour)
	logs_core.SetPatternVersionSinceForTest(since)
	t.Cleanup(func() { logs_core.SetPatternVersionSinceForTest(time.Time{}) })
	now := time.Now().UTC().Truncate(10 * time.Minute)
	from := now.Add(-time.Hour)
	logs := &insightLogs{uniqueID: uniqueID}
	for i := range 20 {
		logs.addMessages(from, 5, "event fresh "+letters(i)+" happened", "checkout")
	}
	for _, name := range []string{"Ada Lovelace", "Grace Hopper", "Alan Turing"} {
		logs.addMessages(from, 4, `order placed {"customer":"`+name+`","city":"Contoso"}`, "checkout")
	}
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.PatternTrendsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("pattern-trends", project.ID), "Bearer "+owner.Token,
		logs_core.PatternTrendsRequestDTO{
			Query:     BuildCondition("test_id", "equals", uniqueID),
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
			Limit:     50,
		}, http.StatusOK, &response)

	require.NotNil(t, response.PatternVersionSince)
	assert.WithinDuration(t, since, *response.PatternVersionSince, time.Second)
	assert.Equal(t, 21, response.Distinct)
	assert.Equal(t, 20, response.NewCount, "the order pattern was three patterns before")
	for _, pattern := range response.Patterns {
		if strings.HasPrefix(pattern.Sample, "order placed") {
			assert.Equal(t, int64(12), pattern.Count)
			assert.Equal(t, "", pattern.Change)
		}
	}
}
