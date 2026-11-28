package main

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

func Test_K8sProfile_StampsEveryLineOnTheSimulatedClock(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	clock := &simClock{start: start, perLine: float64(time.Second) / 1000}
	first := clock.reserve(500)
	second := clock.reserve(500)

	request := buildK8sLogsRequest(rand.New(rand.NewSource(1)), k8sServices(60), clock, second, 500, 4)

	assert.Equal(t, int64(0), first)
	assert.Equal(t, int64(500), second)
	var stamps []uint64
	for _, rl := range request.ResourceLogs {
		for _, r := range rl.ScopeLogs[0].LogRecords {
			stamps = append(stamps, r.TimeUnixNano)
		}
	}
	require.Len(t, stamps, 500)
	lowest, highest := stamps[0], stamps[0]
	for _, s := range stamps {
		lowest, highest = min(lowest, s), max(highest, s)
	}
	assert.Equal(t, uint64(start.Add(500*time.Millisecond).UnixNano()), lowest)
	assert.Equal(t, uint64(start.Add(999*time.Millisecond).UnixNano()), highest)
}

func Test_K8sProfile_PodNames_StayWithinAChurnPeriodAndChangeAfterIt(t *testing.T) {
	svc := k8sServices(120)[0]
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Truncate(svc.churn)

	pod := svc.podAt(0, at)

	assert.Equal(t, pod, svc.podAt(0, at.Add(svc.churn-time.Second)))
	assert.NotEqual(t, pod.name, svc.podAt(0, at.Add(svc.churn)).name)
	assert.NotEqual(t, pod.name, svc.podAt(1, at).name)
	assert.Regexp(t, `^storefront-web-[a-z2-9]{10}-[bcdfghjklmnpqrstvwxz2456789]{5}$`, pod.name)
}

func Test_K8sProfile_Lines_UseOnlyMadeUpNamesAndDocumentationAddresses(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	svc := k8sServices(120)
	ipPattern := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	emailPattern := regexp.MustCompile(`@([a-z0-9.-]+)`)
	kinds := map[k8sLineKind]int{}
	multiline, long := 0, 0

	for i := 0; i < 20_000; i++ {
		s := svc[rng.Intn(len(svc))]
		kind := pickKind(rng, s.kinds)
		kinds[kind]++
		level, body := k8sLine(rng, kind, s.podAt(0, time.Now()), time.Now())

		require.Contains(t, otlpSeverities, level)
		for _, ip := range ipPattern.FindAllString(body, -1) {
			assert.Regexp(t, `^(192\.0\.2|198\.51\.100|203\.0\.113)\.`, ip)
		}
		for _, m := range emailPattern.FindAllStringSubmatch(body, -1) {
			assert.Contains(t, []string{"example.com", "example.net", "example.org"}, m[1])
		}
		if strings.Contains(body, "\n") {
			multiline++
		}
		if len(body) > 1024 {
			long++
		}
	}

	assert.Len(t, kinds, 12, "every kind of line appears")
	assert.Positive(t, multiline)
	assert.Positive(t, long)
}

func Test_K8sProfile_OnceCaughtUp_WaitsForEachBatchsTimestamp(t *testing.T) {
	var batches int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request collectorlogsv1.ExportLogsServiceRequest
		if proto.Unmarshal(body, &request) == nil {
			batches++
		}
		response, _ := proto.Marshal(&collectorlogsv1.ExportLogsServiceResponse{})
		_, _ = w.Write(response)
	}))
	defer server.Close()
	otlp, err := newOTLPSender("logs", "http", server.URL, "", "key", 100, false, false, 5*time.Second)
	require.NoError(t, err)
	sender := newK8sLogsSender(otlp, time.Now(), 500, 30)
	rng := rand.New(rand.NewSource(3))

	began := time.Now()
	for i := 0; i < 3; i++ {
		r := sender.send(context.Background(), rng)
		require.NoError(t, r.err)
		assert.Equal(t, 100, r.accepted)
	}

	// The third batch starts at line 200, 0.4 s into the clock at 500 lines/s.
	assert.GreaterOrEqual(t, time.Since(began), 390*time.Millisecond)
	assert.Equal(t, 3, batches)
}

func Test_K8sProfile_WhenTheSitePushesBack_SendsTheSameBatchAgain(t *testing.T) {
	var calls int
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if calls <= 2 {
			http.Error(w, "ingest buffer is full", http.StatusTooManyRequests)
			return
		}
		response, _ := proto.Marshal(&collectorlogsv1.ExportLogsServiceResponse{})
		_, _ = w.Write(response)
	}))
	defer server.Close()
	otlp, err := newOTLPSender("logs", "http", server.URL, "", "key", 50, false, false, 5*time.Second)
	require.NoError(t, err)
	sender := newK8sLogsSender(otlp, time.Now().Add(-time.Hour), 500, 30)
	sender.backoff = time.Millisecond

	r := sender.send(context.Background(), rand.New(rand.NewSource(5)))

	require.NoError(t, r.err)
	assert.Equal(t, 50, r.accepted)
	assert.Equal(t, 3, calls)
	assert.Equal(t, bodies[0], bodies[2])
	assert.Equal(t, int64(2), sender.retries.Load())
}

func Test_K8sProfile_WhenTheRequestIsRefused_DoesNotRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "invalid API key", http.StatusUnauthorized)
	}))
	defer server.Close()
	otlp, err := newOTLPSender("logs", "http", server.URL, "", "key", 50, false, false, 5*time.Second)
	require.NoError(t, err)
	sender := newK8sLogsSender(otlp, time.Now().Add(-time.Hour), 500, 30)
	sender.backoff = time.Millisecond

	r := sender.send(context.Background(), rand.New(rand.NewSource(5)))

	require.Error(t, r.err)
	assert.Equal(t, 1, calls)
}
