package logs_querier_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_querier "obzly/internal/features/logs/querier"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextFixture: replica a of the checkout deployment writes a log a
// minute from t-3m to t+2m (the anchor at t), its other replica a2 once at
// t-45s, the cart deployment (b) around the anchor, and a has one log far
// outside the default window.
type contextFixture struct {
	projectID uuid.UUID
	querier   *logs_querier.LogQuerier
	anchor    logs_core.LogContextAnchorDTO
}

func seedContext(t *testing.T) contextFixture {
	t.Helper()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	deployments := map[string]string{"pod-a": "checkout", "pod-a2": "checkout", "pod-b": "cart"}
	log := func(offset time.Duration, pod, message string) *logs_core.LogItem {
		return &logs_core.LogItem{
			ID: uuid.New(), Timestamp: at.Add(offset), Level: logs_core.LogLevelInfo, Message: message,
			Fields: map[string]any{
				"k8s_pod_name": pod, "k8s_namespace_name": "shop", "k8s_deployment_name": deployments[pod],
				"host_name": "node-" + pod,
			},
		}
	}
	anchor := log(0, "pod-a", "a 0")
	anchor.Level, anchor.TraceID = logs_core.LogLevelError, "trace-1"
	sameTrace := log(30*time.Second, "pod-b", "b +30s")
	sameTrace.TraceID = "trace-1"
	items := []*logs_core.LogItem{
		log(-3*time.Minute, "pod-a", "a -3m"),
		log(-2*time.Minute, "pod-a", "a -2m"),
		log(-time.Minute, "pod-a", "a -1m"),
		log(-45*time.Second, "pod-a2", "a2 -45s"),
		log(-30*time.Second, "pod-b", "b -30s"),
		anchor,
		sameTrace,
		log(time.Minute, "pod-a", "a +1m"),
		log(2*time.Minute, "pod-a", "a +2m"),
		log(-2*time.Hour, "pod-a", "a -2h"),
	}
	projectID, querier := seedProject(t, items)
	return contextFixture{projectID, querier,
		logs_core.LogContextAnchorDTO{ID: anchor.ID.String(), Timestamp: anchor.Timestamp}}
}

func (f contextFixture) run(t *testing.T, request *logs_core.LogContextRequestDTO) *logs_core.LogContextResponseDTO {
	t.Helper()
	request.Anchor = f.anchor
	response, err := f.querier.ExecuteContext(context.Background(), f.projectID, request)
	require.NoError(t, err)
	return response
}

func Test_ExecuteContext_DefaultScope_TheDeploymentsReplicasOldestFirstWithoutTheAnchor(t *testing.T) {
	f := seedContext(t)

	response := f.run(t, &logs_core.LogContextRequestDTO{Before: 2, After: 1})

	require.NotNil(t, response.Anchor)
	assert.Equal(t, "a 0", response.Anchor.Message)
	assert.Equal(t, []string{"a -1m", "a2 -45s"}, messagesOf(response.Before), "both replicas, not the cart")
	assert.Equal(t, []string{"a +1m"}, messagesOf(response.After))
	assert.Equal(t, []logs_core.LogContextScopeDTO{
		{Field: "k8s_namespace_name", Value: "shop"}, {Field: "k8s_deployment_name", Value: "checkout"},
	}, response.Scope, "the workload, not the pod or its host")
	assert.NotEmpty(t, response.BeforeCursor, "a full side can load more")
	assert.NotEmpty(t, response.AfterCursor)
	assert.Equal(t, 60, response.WindowMinutes)
}

func Test_ExecuteContext_DefaultExtraFields_JoinTheDefaultScope(t *testing.T) {
	f := seedContext(t)

	response := f.run(t, &logs_core.LogContextRequestDTO{DefaultExtraFields: []string{"k8s_pod_name"}, Before: 1})

	assert.Equal(t, []string{"a -1m"}, messagesOf(response.Before))
	assert.Contains(t, response.Scope, logs_core.LogContextScopeDTO{Field: "k8s_pod_name", Value: "pod-a"})
}

func Test_ExecuteContext_LoadMore_ContinuesEachSideFromItsCursor(t *testing.T) {
	f := seedContext(t)
	first := f.run(t, &logs_core.LogContextRequestDTO{Before: 2, After: 1})

	earlier := f.run(t, &logs_core.LogContextRequestDTO{
		Scope: fieldsOf(first.Scope), Before: 5, Direction: logs_core.ContextBefore, Cursor: first.BeforeCursor,
	})
	later := f.run(t, &logs_core.LogContextRequestDTO{
		Scope: fieldsOf(first.Scope), After: 5, Direction: logs_core.ContextAfter, Cursor: first.AfterCursor,
	})

	assert.Equal(t, []string{"a -3m", "a -2m"}, messagesOf(earlier.Before), "the 2-hour-old log is outside the window")
	assert.Empty(t, earlier.After, "one side only")
	assert.Empty(t, earlier.BeforeCursor, "a short side has nothing more")
	assert.Equal(t, []string{"a +2m"}, messagesOf(later.After))
}

func Test_ExecuteContext_ScopeChoices(t *testing.T) {
	f := seedContext(t)

	whole := f.run(t, &logs_core.LogContextRequestDTO{Scope: []string{}, Before: 2, After: 1})
	assert.Equal(t, []string{"a2 -45s", "b -30s"}, messagesOf(whole.Before), "[] is the whole project")
	assert.Equal(t, []string{"b +30s"}, messagesOf(whole.After))
	assert.Empty(t, whole.Scope)

	pod := f.run(t, &logs_core.LogContextRequestDTO{Scope: []string{"k8s_pod_name"}, Before: 1})
	assert.Equal(t, []string{"a -1m"}, messagesOf(pod.Before), "one replica")

	trace := f.run(t, &logs_core.LogContextRequestDTO{Scope: []string{"trace_id"}})
	assert.Empty(t, trace.Before)
	assert.Equal(t, []string{"b +30s"}, messagesOf(trace.After))
	assert.Equal(t, []logs_core.LogContextScopeDTO{{Field: "trace_id", Value: "trace-1"}}, trace.Scope)

	missing := f.run(t, &logs_core.LogContextRequestDTO{Scope: []string{"service_name", "k8s_pod_name"}, Before: 1})
	assert.Equal(t, []logs_core.LogContextScopeDTO{{Field: "k8s_pod_name", Value: "pod-a"}}, missing.Scope,
		"a field the anchor lacks is dropped, not matched as empty")
	assert.Equal(t, []string{"a -1m"}, messagesOf(missing.Before))
}

func Test_ExecuteContext_WiderWindow_ReachesOlderLogs(t *testing.T) {
	f := seedContext(t)

	response := f.run(t, &logs_core.LogContextRequestDTO{Before: 10, WindowMinutes: 180})

	assert.Equal(t, []string{"a -2h", "a -3m", "a -2m", "a -1m", "a2 -45s"}, messagesOf(response.Before))
	assert.Empty(t, response.BeforeCursor)
}

func fieldsOf(scope []logs_core.LogContextScopeDTO) []string {
	fields := make([]string, 0, len(scope))
	for _, s := range scope {
		fields = append(fields, s.Field)
	}
	return fields
}

func Test_ExecuteContext_LogsSharingTheAnchorTimestamp_SplitByID(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var items []*logs_core.LogItem
	for range 5 {
		items = append(items, &logs_core.LogItem{ID: uuid.New(), Timestamp: at, Level: logs_core.LogLevelInfo,
			Message: "same instant"})
	}
	projectID, querier := seedProject(t, items)
	anchor := items[2]

	response, err := querier.ExecuteContext(context.Background(), projectID, &logs_core.LogContextRequestDTO{
		Anchor: logs_core.LogContextAnchorDTO{ID: anchor.ID.String(), Timestamp: at},
	})
	require.NoError(t, err)

	var ids []string
	for _, item := range append(response.Before, response.After...) {
		ids = append(ids, item.ID)
		if item.ID < anchor.ID.String() {
			assert.Contains(t, response.Before, item)
		} else {
			assert.Contains(t, response.After, item)
		}
	}
	assert.Len(t, ids, 4, "every other log at that instant, once")
	assert.NotContains(t, ids, anchor.ID.String())
}

func Test_ExecuteContext_AnchorGone_ReturnsNoAnchorAndNoRows(t *testing.T) {
	f := seedContext(t)
	f.anchor.ID = uuid.NewString()

	response := f.run(t, &logs_core.LogContextRequestDTO{})

	assert.Nil(t, response.Anchor)
	assert.Empty(t, response.Before)
	assert.Empty(t, response.After)
}

// A time.Time parameter binds at microsecond precision, so a nanosecond
// timestamp never equaled it: the anchor wasn't found and pages skipped rows.
func Test_ExecuteContext_NanosecondTimestamps_FindTheAnchorAndSplitExactly(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(123_456_789)
	var items []*logs_core.LogItem
	for i, offset := range []time.Duration{-2, -1, 0, 0, 0, 1, 2} {
		items = append(items, &logs_core.LogItem{ID: uuid.New(), Timestamp: at.Add(offset * time.Nanosecond),
			Level: logs_core.LogLevelInfo, Message: fmt.Sprintf("log %d", i)})
	}
	projectID, querier := seedProject(t, items)
	anchor := items[3]

	response, err := querier.ExecuteContext(context.Background(), projectID, &logs_core.LogContextRequestDTO{
		Anchor: logs_core.LogContextAnchorDTO{ID: anchor.ID.String(), Timestamp: anchor.Timestamp},
	})
	require.NoError(t, err)

	require.NotNil(t, response.Anchor)
	assert.Equal(t, anchor.Timestamp, response.Anchor.Timestamp)
	got := append(messagesOf(response.Before), messagesOf(response.After)...)
	assert.ElementsMatch(t, []string{"log 0", "log 1", "log 2", "log 4", "log 5", "log 6"}, got,
		"every other log once, never the anchor")
	assert.Equal(t, []string{"log 0", "log 1"}, messagesOf(response.Before)[:2])
	assert.Equal(t, []string{"log 5", "log 6"}, messagesOf(response.After)[len(response.After)-2:])
}

// The same precision gap in search paging: logs sharing a nanosecond
// timestamp across a page boundary were skipped or repeated.
func Test_ExecuteQuery_CursorPages_NanosecondTimestamps_ReturnEveryLogOnce(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(987_654_321)
	var items []*logs_core.LogItem
	for i, offset := range []time.Duration{-1, 0, 0, 0, 0, 0, 1} {
		items = append(items, &logs_core.LogItem{ID: uuid.New(), Timestamp: at.Add(offset * time.Nanosecond),
			Level: logs_core.LogLevelInfo, Message: fmt.Sprintf("log %d", i)})
	}
	projectID, querier := seedProject(t, items)

	request := queryRequest(nil, at.Add(-time.Minute), at.Add(time.Minute))
	request.Limit = 2
	seen := map[string]int{}
	for range 10 {
		response, err := querier.ExecuteQuery(projectID, request)
		require.NoError(t, err)
		for _, log := range response.Logs {
			seen[log.Message]++
		}
		if response.NextCursor == "" {
			break
		}
		request.Cursor = response.NextCursor
	}

	require.Len(t, seen, len(items), "every log")
	for message, count := range seen {
		assert.Equal(t, 1, count, "%s returned once", message)
	}
}
