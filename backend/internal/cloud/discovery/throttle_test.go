package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Throttle_On429WithRetryAfter_StandsDownAndRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, `{"query":"Resources"}`, string(body), "the body is sent again on the retry")
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	ctx, stats := withStats(context.Background())
	client := NewThrottle().client(nil)
	request, _ := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL,
		strings.NewReader(`{"query":"Resources"}`),
	)
	started := time.Now()
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.GreaterOrEqual(t, time.Since(started), time.Second, "waits as long as it was told")
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, stats.throttled.Load())
	require.GreaterOrEqual(t, time.Duration(stats.stoodDown.Load()), 900*time.Millisecond)
}

func Test_Throttle_WhenAzureQuotaRunsOut_WaitsForTheReset(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("x-ms-user-quota-remaining", "0")
			w.Header().Set("x-ms-user-quota-resets-after", "00:00:01")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewThrottle().client(nil)
	started := time.Now()
	for range 2 {
		response, err := client.Get(server.URL + "/providers/Microsoft.ResourceGraph/resources")
		require.NoError(t, err)
		_ = response.Body.Close()
	}
	require.GreaterOrEqual(t, time.Since(started), time.Second, "the second query waits for the quota")
}

func Test_Throttle_AWSThrottlingError_IsCounted_AndLeftToTheSDK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>Throttling</Code><Message>Rate exceeded</Message>` +
			`</Error></ErrorResponse>`))
	}))
	defer server.Close()

	ctx, stats := withStats(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := NewThrottle().client(nil).Do(request)
	require.NoError(t, err)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()

	require.Contains(t, string(body), "<Code>Throttling</Code>", "the SDK still reads the error")
	require.EqualValues(t, 1, stats.throttled.Load())
}

func Test_Throttle_AWS429_StandsDownOnly_AndLeavesRetriesToTheSDK(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	throttle := NewThrottle()
	ctx, stats := withStats(context.WithValue(context.Background(), awsKey{}, true))
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := throttle.client(nil).Do(request)
	require.NoError(t, err)
	_ = response.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, response.StatusCode, "the SDK sees the 429 and retries itself")
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 1, stats.throttled.Load())
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	for _, endpoint := range throttle.endpoints {
		require.True(t, endpoint.until.After(time.Now()), "the endpoint still stands down")
	}
}

func Test_RetryAfter_And_QuotaResetsAfter_Parse(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 7*time.Second, retryAfter(http.Header{"Retry-After": {"7"}}, now))
	require.Equal(t, 30*time.Second,
		retryAfter(http.Header{"Retry-After": {now.Add(30 * time.Second).Format(http.TimeFormat)}}, now))
	require.Equal(t, 1500*time.Millisecond, retryAfter(http.Header{"X-Ms-Retry-After-Ms": {"1500"}}, now))
	require.Equal(t, 65*time.Second, quotaResetsAfter("00:01:05"))
	require.Zero(t, quotaResetsAfter("soon"))
}
