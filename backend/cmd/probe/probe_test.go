package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/report"
	"obzly/internal/synthetics/spec"
)

func Test_Apply_KeepsUnchangedChecks_RestartsChanged_StopsRemoved(t *testing.T) {
	agent := testProbe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := testConfig(spec.ProbeCheck{ID: "a", Name: "A", IntervalSeconds: 3600, DestinationID: "d"},
		spec.ProbeCheck{ID: "b", Name: "B", IntervalSeconds: 3600, DestinationID: "d"})

	agent.apply(ctx, config, `"1"`)
	first := agent.checks["a"].fingerprint
	unchangedB := agent.checks["b"]
	changed := testConfig(spec.ProbeCheck{ID: "a", Name: "A", IntervalSeconds: 60, DestinationID: "d"},
		spec.ProbeCheck{ID: "b", Name: "B", IntervalSeconds: 3600, DestinationID: "d"})
	agent.apply(ctx, changed, `"2"`)

	require.NotEqual(t, first, agent.checks["a"].fingerprint)
	require.Same(t, unchangedB, agent.checks["b"])
	agent.apply(ctx, testConfig(), `"3"`)
	require.Empty(t, agent.checks)
}

func Test_Apply_CheckWithoutItsDestination_IsNotRun(t *testing.T) {
	agent := testProbe(t)

	agent.apply(
		context.Background(),
		testConfig(spec.ProbeCheck{ID: "a", DestinationID: "missing", IntervalSeconds: 60}),
		`"1"`,
	)

	require.Empty(t, agent.checks)
}

func Test_Sender_SiteDown_KeepsResultsAndSendsThemLater(t *testing.T) {
	var up atomic.Bool
	var received atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/v1/logs" {
			received.Add(1)
		}
	}))
	defer site.Close()
	sender := newSender(&report.Sender{Client: site.Client()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	destination := spec.Destination{ID: "d", OTLPURL: site.URL}

	sender.add(destination, spec.ProbeInfo{}, spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	sender.flush(context.Background(), false)
	require.Len(t, sender.pending["d"].results, 1, "kept for the next try")
	require.Equal(t, firstBackoff, sender.pending["d"].backoff)
	sender.pending["d"].retryAt = time.Time{}
	sender.flush(context.Background(), false)
	require.Equal(t, 2*firstBackoff, sender.pending["d"].backoff, "the wait doubles")
	up.Store(true)
	sender.flush(context.Background(), false)
	require.Len(t, sender.pending["d"].results, 1, "still waiting out the backoff")
	sender.flush(context.Background(), true)

	require.Empty(t, sender.pending["d"].results)
	require.Equal(t, int64(1), received.Load())
	require.Zero(t, sender.pending["d"].backoff, "reset once the Site takes them")
}

func Test_Sender_ManyResults_PostsChunksOf500(t *testing.T) {
	var metricsPosts, logsPosts atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/metrics" {
			metricsPosts.Add(1)
		} else {
			logsPosts.Add(1)
		}
	}))
	defer site.Close()
	sender := testSender(site)
	destination := spec.Destination{ID: "d", OTLPURL: site.URL}

	for range 1200 {
		sender.add(destination, spec.ProbeInfo{}, spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	}
	sender.flush(context.Background(), false)

	require.Equal(t, int64(3), metricsPosts.Load())
	require.Equal(t, int64(3), logsPosts.Load())
	require.Empty(t, sender.pending["d"].results)
}

func Test_Sender_SiteRefusesWith4xx_DropsTheChunk_ButKeepsItOn429(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusUnauthorized)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer site.Close()
	sender := testSender(site)
	destination := spec.Destination{ID: "d", OTLPURL: site.URL}
	result := spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()}

	sender.add(destination, spec.ProbeInfo{}, result)
	sender.flush(context.Background(), false)
	require.Empty(t, sender.pending["d"].results, "a 401 won't change on retry")
	require.Zero(t, sender.pending["d"].backoff)

	status.Store(http.StatusTooManyRequests)
	sender.add(destination, spec.ProbeInfo{}, result)
	sender.flush(context.Background(), false)
	require.Len(t, sender.pending["d"].results, 1)
}

func Test_Sender_LogsPostFails_TheRetrySendsOnlyLogs(t *testing.T) {
	var metricsPoints, logsPosts atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/metrics" {
			metricsPoints.Add(1)
			return
		}
		if logsPosts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer site.Close()
	sender := testSender(site)
	destination := spec.Destination{ID: "d", OTLPURL: site.URL}

	sender.add(destination, spec.ProbeInfo{}, spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	sender.flush(context.Background(), false)
	sender.add(destination, spec.ProbeInfo{}, spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	sender.flush(context.Background(), true)

	require.Equal(t, int64(2), metricsPoints.Load(), "one post per result, none twice")
	require.Equal(t, int64(3), logsPosts.Load(), "the failed one again, then the new one")
	require.Empty(t, sender.pending["d"].results)
}

func Test_Sender_Flush_SendsToDestinationsConcurrently(t *testing.T) {
	bothArrived := make(chan struct{})
	var arrived atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			return
		}
		if arrived.Add(1) == 2 {
			close(bothArrived)
		}
		select {
		case <-bothArrived:
		case <-time.After(3 * time.Second):
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer site.Close()
	sender := testSender(site)

	for _, id := range []string{"a", "b"} {
		sender.add(spec.Destination{ID: id, OTLPURL: site.URL + "/" + id}, spec.ProbeInfo{},
			spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	}
	sender.flush(context.Background(), false)

	require.Empty(t, sender.pending["a"].results, "a slow destination doesn't hold up another")
	require.Empty(t, sender.pending["b"].results)
}

func Test_Sender_AResultFromAnOlderConfig_GoesWithTheCurrentKey(t *testing.T) {
	var key atomic.Value
	site := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		key.Store(r.Header.Get("X-API-Key"))
	}))
	defer site.Close()
	sender := testSender(site)
	rotated := spec.Destination{ID: "d", OTLPURL: site.URL, APIKey: "lb_rotated"}

	sender.setDestination(rotated, spec.ProbeInfo{})
	sender.add(spec.Destination{ID: "d", OTLPURL: site.URL, APIKey: "lb_revoked"}, spec.ProbeInfo{},
		spec.RunResult{Status: spec.StatusUp, StartedAt: time.Now()})
	sender.flush(context.Background(), false)

	require.Equal(t, "lb_rotated", key.Load())
}

func Test_NextRun_EveryReplicaAndRestartAgrees_AlignedToTheClock(t *testing.T) {
	interval := 3 * time.Minute
	now := time.Date(2026, 11, 4, 10, 7, 13, 0, time.UTC)

	next := nextRun(now, "check-a", interval)
	later := nextRun(now.Add(37*time.Second), "check-a", interval)

	require.True(t, next.After(now))
	require.LessOrEqual(t, next.Sub(now), interval)
	if later.Before(next.Add(time.Nanosecond)) {
		require.Equal(t, next, later, "a loop restarted before the slot runs at it")
	} else {
		require.Equal(t, next.Add(interval), later, "a loop restarted after the slot waits for the next one")
	}
	require.Equal(t, next.Add(interval), nextRun(next, "check-a", interval), "a run at its slot never repeats it")
	require.Equal(t, next.Add(interval), nextRun(next.Add(time.Millisecond), "check-a", interval))
	require.Equal(t, next.UnixNano()%int64(interval), nextRun(now.Add(5*interval), "check-a", interval).
		UnixNano()%int64(interval), "the same offset in every interval")
	offsets := map[int64]bool{}
	for i := range 20 {
		offsets[nextRun(now, fmt.Sprintf("check-%d", i), interval).UnixNano()%int64(interval)] = true
	}
	require.Greater(t, len(offsets), 15, "checks spread over the interval")
}

func Test_RunTask_EveryScheduledSlotBusy_TheTestStillRuns(t *testing.T) {
	posted := make(chan spec.RunResult, 1)
	controlPlaneServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var result spec.RunResult
		_ = json.NewDecoder(r.Body).Decode(&result)
		posted <- result
	}))
	defer controlPlaneServer.Close()
	agent := testProbe(t)
	agent.api = &controlPlane{base: controlPlaneServer.URL, token: "obz_prb_test", http: controlPlaneServer.Client()}
	agent.policy = func(net.IP) error { return nil }
	agent.slots <- struct{}{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	go agent.runTask(context.Background(), spec.Task{ID: "t1", Check: spec.ProbeCheck{ID: "c1", Definition: spec.Definition{
		Type: spec.TypeTCP, Target: listener.Addr().String(), TimeoutSeconds: 2,
	}}})

	select {
	case result := <-posted:
		require.Equal(t, spec.StatusUp, result.Status, result.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("the test waited for a scheduled check's slot")
	}
}

func Test_Loop_StoppedMidRun_TheRunFinishesAndItsResultIsKept(t *testing.T) {
	release := make(chan struct{})
	site := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	defer site.Close()
	agent := testProbe(t)
	agent.policy = func(net.IP) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	check := spec.ProbeCheck{IntervalSeconds: spec.MinIntervalSeconds, Definition: spec.Definition{
		Type: spec.TypeHTTP, TimeoutSeconds: 5, Request: &spec.HTTPRequest{Method: "GET", URL: site.URL},
	}}
	for i := 0; check.ID == ""; i++ {
		id := fmt.Sprintf("check-%d", i)
		if wait := time.Until(nextRun(time.Now(), id, spec.MinIntervalSeconds*time.Second)); wait < time.Second {
			check.ID = id
		}
	}

	go agent.loop(ctx, check, spec.Destination{ID: "d"}, spec.ProbeInfo{})
	require.Eventually(t, func() bool { return agent.running.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	close(release)
	agent.waitForRuns(5 * time.Second)

	agent.sender.mu.Lock()
	defer agent.sender.mu.Unlock()
	require.Len(t, agent.sender.pending["d"].results, 1)
	require.Equal(t, spec.StatusUp, agent.sender.pending["d"].results[0].Status)
}

func testSender(site *httptest.Server) *sender {
	return newSender(&report.Sender{Client: site.Client()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func testProbe(t *testing.T) *probe {
	t.Helper()
	return &probe{
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		slots:     make(chan struct{}, 1),
		testSlots: make(chan struct{}, maxTestRuns),
		checks:    map[string]*scheduled{},
		sender: newSender(
			&report.Sender{Client: http.DefaultClient},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		),
		cachePath: t.TempDir() + "/config.json",
	}
}

func testConfig(checks ...spec.ProbeCheck) *spec.ProbeConfig {
	return &spec.ProbeConfig{
		Probe:        spec.ProbeInfo{ID: "p", Name: "probe", Location: "office-blr"},
		Destinations: []spec.Destination{{ID: "d", OTLPURL: "https://site.example.com"}},
		Checks:       checks,
	}
}

func Test_Owner_SpreadsMonitors_AndOnlyALeaversMove(t *testing.T) {
	members := []string{"probe-0", "probe-1", "probe-2"}
	counts := map[string]int{}
	before := map[string]string{}
	for i := range 300 {
		id := fmt.Sprintf("check-%d", i)
		before[id] = owner(id, members, "probe-0")
		counts[before[id]]++
		require.Equal(t, before[id], owner(id, members, "probe-1"), "every member agrees")
	}
	for _, member := range members {
		require.Greater(t, counts[member], 70, "each of 3 members runs about a third")
	}

	left := []string{"probe-0", "probe-2"}
	for id, was := range before {
		now := owner(id, left, "probe-0")
		if was != "probe-1" {
			require.Equal(t, was, now, "monitors of members that stayed don't move")
		} else {
			require.Contains(t, left, now)
		}
	}
	require.Equal(t, "solo", owner("check-1", nil, "solo"), "an instance not yet listed runs its share")
}
