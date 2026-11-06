// Command loadtest is a load-testing tool for Obzly's ingestion paths. It only
// calls the endpoints a real client uses, so results reflect what users see.
// Modes:
//   - rest (default): POST /api/v1/logs/receiving/:projectId (JSON logs).
//   - otlp: OTLP logs, traces or metrics (-signal) over HTTP protobuf or
//     gRPC (-protocol), optionally gzip-compressed like the OTel Collector's
//     default. The API key selects the project.
//
// It does NOT create or configure a project. For a clean throughput number,
// raise or disable the project's logsPerSecondLimit first, or pass -rps to
// pace the send rate under the configured limit.
//
// Usage:
//
//	go run ./cmd/loadtest -base-url http://localhost:4005 -project-id <uuid> \
//	  -api-key <key> -duration 30s -concurrency 20 -batch-size 100
//
//	go run ./cmd/loadtest -mode otlp -signal traces -protocol grpc \
//	  -grpc-addr localhost:4317 -api-key <key> -gzip
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// logItem duplicates logs_receiving.LogItemRequestDTO's JSON shape: this is an
// external client and doesn't import internal packages.
type logItem struct {
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
}

type submitRequest struct {
	Logs []logItem `json:"logs"`
}

type submitResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Errors   []struct {
		Index   int    `json:"index"`
		Message string `json:"message"`
	} `json:"errors"`
}

var (
	levels   = []string{"DEBUG", "INFO", "INFO", "INFO", "WARN", "ERROR"}
	messages = []string{
		"request completed",
		"cache miss, fetching from upstream",
		"user authentication succeeded",
		"failed to acquire lock, retrying",
		"database query took longer than expected",
		"connection pool exhausted",
		"scheduled job started",
		"scheduled job finished",
		"payment processed successfully",
		"rate limit threshold approaching",
	}
	services = []string{"api-gateway", "auth-service", "billing-service", "worker", "scheduler"}
)

// buildLogItem varies size and content: a repeated fixed string would compress
// away and understate real ingestion cost.
func buildLogItem(rng *rand.Rand) logItem {
	return logItem{
		Level:   levels[rng.Intn(len(levels))],
		Message: messages[rng.Intn(len(messages))],
		Fields: map[string]any{
			"service":     services[rng.Intn(len(services))],
			"request_id":  fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), rng.Int63()),
			"duration_ms": rng.Intn(2000),
			"status_code": []int{200, 200, 200, 201, 400, 404, 500}[rng.Intn(7)],
			"user_id":     fmt.Sprintf("user-%d", rng.Intn(10000)),
		},
	}
}

// sender sends one batch per call; each mode implements it.
type sender interface {
	send(ctx context.Context, rng *rand.Rand) result
	target() string
	unit() string
	close()
}

type restSender struct {
	client    *http.Client
	url       string
	apiKey    string
	batchSize int
}

func (s *restSender) send(ctx context.Context, rng *rand.Rand) result {
	return sendBatch(ctx, s.client, s.url, s.apiKey, s.batchSize, rng)
}
func (s *restSender) target() string { return s.url }
func (s *restSender) unit() string   { return "logs" }
func (s *restSender) close()         {}

type result struct {
	latency        time.Duration
	sentBytes      int
	accepted       int
	rejected       int
	err            error
	httpStatusCode int
}

func main() {
	mode := flag.String("mode", "rest", "rest (JSON logs API) or otlp")
	signal := flag.String("signal", "logs", "otlp mode: logs, traces or metrics")
	protocol := flag.String("protocol", "http", "otlp mode: http (protobuf) or grpc")
	baseURL := flag.String("base-url", "http://localhost:4005", "Obzly server base URL (rest and OTLP/HTTP)")
	grpcAddr := flag.String("grpc-addr", "localhost:4317", "OTLP/gRPC host:port")
	grpcTLS := flag.Bool("grpc-tls", false, "use TLS for OTLP/gRPC")
	useGzip := flag.Bool("gzip", false, "otlp mode: gzip-compress requests (the OTel Collector default)")
	projectID := flag.String("project-id", "", "target project ID (rest mode only)")
	apiKey := flag.String("api-key", "", "project API key (required for otlp; rest only if the project requires one)")
	duration := flag.Duration("duration", 30*time.Second, "how long to run the load test")
	concurrency := flag.Int("concurrency", 20, "number of concurrent workers")
	batchSize := flag.Int("batch-size", 100, "logs, spans or data points per request (server caps at 5000)")
	rps := flag.Int("rps", 0, "cap this tool's own send rate in requests/sec, 0 = unlimited (send as fast as possible)")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	flag.Parse()

	if *batchSize < 1 || *batchSize > 5000 {
		fmt.Fprintln(os.Stderr, "error: -batch-size must be between 1 and 5000 (server-enforced MaxBatchSize)")
		os.Exit(2)
	}

	var s sender
	switch *mode {
	case "rest":
		if *projectID == "" {
			fmt.Fprintln(os.Stderr, "error: -project-id is required in rest mode")
			flag.Usage()
			os.Exit(2)
		}
		s = &restSender{
			client:    &http.Client{Timeout: *timeout},
			url:       fmt.Sprintf("%s/api/v1/logs/receiving/%s", *baseURL, *projectID),
			apiKey:    *apiKey,
			batchSize: *batchSize,
		}
	case "otlp":
		if *signal != "logs" && *signal != "traces" && *signal != "metrics" {
			fmt.Fprintln(os.Stderr, "error: -signal must be logs, traces or metrics")
			os.Exit(2)
		}
		if *protocol != "http" && *protocol != "grpc" {
			fmt.Fprintln(os.Stderr, "error: -protocol must be http or grpc")
			os.Exit(2)
		}
		if *apiKey == "" {
			fmt.Fprintln(os.Stderr, "error: -api-key is required in otlp mode (it selects the project)")
			os.Exit(2)
		}
		otlp, err := newOTLPSender(
			*signal,
			*protocol,
			*baseURL,
			*grpcAddr,
			*apiKey,
			*batchSize,
			*useGzip,
			*grpcTLS,
			*timeout,
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		s = otlp
	default:
		fmt.Fprintln(os.Stderr, "error: -mode must be rest or otlp")
		os.Exit(2)
	}
	defer s.close()

	fmt.Printf(
		"Load testing %s\n  duration=%s concurrency=%d batch-size=%d rps-cap=%s gzip=%t\n\n",
		s.target(), *duration, *concurrency, *batchSize, rpsLabel(*rps), *useGzip && *mode == "otlp",
	)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	resultsCh := make(chan result, *concurrency*2)
	var wg sync.WaitGroup

	var ticker *time.Ticker
	if *rps > 0 {
		perWorkerInterval := time.Duration(float64(*concurrency) / float64(*rps) * float64(time.Second))
		if perWorkerInterval <= 0 {
			perWorkerInterval = time.Millisecond
		}
		ticker = time.NewTicker(perWorkerInterval)
		defer ticker.Stop()
	}

	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if ticker != nil {
					select {
					case <-ticker.C:
					case <-ctx.Done():
						return
					}
				}
				reqCtx, reqCancel := context.WithTimeout(ctx, *timeout)
				r := s.send(reqCtx, rng)
				reqCancel()
				if ctx.Err() != nil && r.err != nil {
					return // cut off by the end of the run, not a real failure
				}
				resultsCh <- r
			}
		}(w)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var (
		totalRequests  int64
		totalAccepted  int64
		totalRejected  int64
		totalErrors    int64
		totalBytes     int64
		statusCounts   = map[int]int64{}
		latencies      []time.Duration
		latenciesMutex sync.Mutex
		sampleErrors   []string
	)
	const maxSampleErrors = 10

	for r := range resultsCh {
		atomic.AddInt64(&totalRequests, 1)
		if r.err != nil {
			atomic.AddInt64(&totalErrors, 1)
			latenciesMutex.Lock()
			if len(sampleErrors) < maxSampleErrors {
				sampleErrors = append(sampleErrors, r.err.Error())
			}
			latenciesMutex.Unlock()
			continue
		}
		atomic.AddInt64(&totalAccepted, int64(r.accepted))
		atomic.AddInt64(&totalRejected, int64(r.rejected))
		atomic.AddInt64(&totalBytes, int64(r.sentBytes))
		latenciesMutex.Lock()
		latencies = append(latencies, r.latency)
		statusCounts[r.httpStatusCode]++
		latenciesMutex.Unlock()
	}
	elapsed := time.Since(start)

	printReport(reportInput{
		elapsed:       elapsed,
		totalRequests: totalRequests,
		totalAccepted: totalAccepted,
		totalRejected: totalRejected,
		totalErrors:   totalErrors,
		totalBytes:    totalBytes,
		statusCounts:  statusCounts,
		latencies:     latencies,
		sampleErrors:  sampleErrors,
		unit:          s.unit(),
	})
}

func sendBatch(ctx context.Context, client *http.Client, url, apiKey string, batchSize int, rng *rand.Rand) result {
	logs := make([]logItem, batchSize)
	for i := range logs {
		logs[i] = buildLogItem(rng)
	}
	body, err := json.Marshal(submitRequest{Logs: logs})
	if err != nil {
		return result{err: fmt.Errorf("marshal request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return result{err: fmt.Errorf("request failed: %w", err), latency: latency}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return result{
			err:            fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
			latency:        latency,
			httpStatusCode: resp.StatusCode,
		}
	}

	var parsed submitResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return result{err: fmt.Errorf("unmarshal response: %w", err), latency: latency}
	}

	return result{
		latency:        latency,
		sentBytes:      len(body),
		accepted:       parsed.Accepted,
		rejected:       parsed.Rejected,
		httpStatusCode: resp.StatusCode,
	}
}

type reportInput struct {
	elapsed       time.Duration
	totalRequests int64
	totalAccepted int64
	totalRejected int64
	totalErrors   int64
	totalBytes    int64
	statusCounts  map[int]int64
	latencies     []time.Duration
	sampleErrors  []string
	unit          string
}

func printReport(in reportInput) {
	sort.Slice(in.latencies, func(i, j int) bool { return in.latencies[i] < in.latencies[j] })

	fmt.Println("=== Results ===")
	fmt.Printf("Elapsed:           %s\n", in.elapsed.Round(time.Millisecond))
	fmt.Printf("Total requests:    %d (%d errored)\n", in.totalRequests, in.totalErrors)
	fmt.Printf("Accepted:          %d %s\n", in.totalAccepted, in.unit)
	fmt.Printf("Rejected:          %d %s\n", in.totalRejected, in.unit)
	fmt.Printf("Bytes sent:        %.2f MB\n", float64(in.totalBytes)/1024/1024)
	fmt.Println()
	fmt.Printf("Throughput:        %.1f %s/sec, %.2f MB/sec, %.1f req/sec\n",
		float64(in.totalAccepted)/in.elapsed.Seconds(), in.unit,
		float64(in.totalBytes)/1024/1024/in.elapsed.Seconds(),
		float64(in.totalRequests)/in.elapsed.Seconds(),
	)
	fmt.Println()
	if len(in.latencies) > 0 {
		fmt.Println("Request latency (successful requests only):")
		fmt.Printf("  min: %s\n", in.latencies[0].Round(time.Millisecond))
		fmt.Printf("  p50: %s\n", percentile(in.latencies, 50).Round(time.Millisecond))
		fmt.Printf("  p90: %s\n", percentile(in.latencies, 90).Round(time.Millisecond))
		fmt.Printf("  p99: %s\n", percentile(in.latencies, 99).Round(time.Millisecond))
		fmt.Printf("  max: %s\n", in.latencies[len(in.latencies)-1].Round(time.Millisecond))
	}
	if len(in.statusCounts) > 0 {
		fmt.Println()
		fmt.Println("HTTP status codes:")
		for code, count := range in.statusCounts {
			fmt.Printf("  %d: %d\n", code, count)
		}
	}
	if len(in.sampleErrors) > 0 {
		fmt.Println()
		fmt.Printf("Sample errors (first %d):\n", len(in.sampleErrors))
		for _, e := range in.sampleErrors {
			fmt.Printf("  - %s\n", e)
		}
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func rpsLabel(rps int) string {
	if rps <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d", rps)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
