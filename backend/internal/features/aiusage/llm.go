package aiusage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

const (
	maxLLMBreakdownRows = 100
	defaultLLMCalls     = 20
	maxLLMCalls         = 100
)

// llmGroupColumns are the breakdown dimensions (columns of "llm").
var llmGroupColumns = map[string]string{
	"service":   "service",
	"model":     "model",
	"provider":  "provider",
	"operation": "operation",
}

type LLMOverviewRequest struct {
	From          time.Time
	To            time.Time
	GroupBy       string
	BucketSeconds int64
}

type LLMSummaryDTO struct {
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CachedTokens int64   `json:"cachedTokens"`
	Errors       int64   `json:"errors"`
	P50Ms        float64 `json:"p50Ms"`
	P95Ms        float64 `json:"p95Ms"`
	Services     int64   `json:"services"`
	// CostUSD covers models with a price only (see UnpricedModels).
	CostUSD float64 `json:"costUsd"`
	// UnpricedModels used tokens but have no price.
	UnpricedModels []string `json:"unpricedModels"`
}

type LLMTimeseriesPointDTO struct {
	// Bucket start, Unix milliseconds.
	Bucket       int64  `json:"bucket"`
	Model        string `json:"model"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
}

type LLMBreakdownRowDTO struct {
	Key          string  `json:"key"`
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Errors       int64   `json:"errors"`
	P95Ms        float64 `json:"p95Ms"`
	// CostUSD is nil when no tokens of the group have a price.
	CostUSD *float64 `json:"costUsd"`
	// HasTokens: some calls reported tokens (tool and agent spans don't).
	HasTokens bool `json:"hasTokens"`
	// Unpriced: some tokens are of a model without a price.
	Unpriced bool `json:"unpriced"`
}

type LLMOverviewResponseDTO struct {
	Summary       LLMSummaryDTO           `json:"summary"`
	Timeseries    []LLMTimeseriesPointDTO `json:"timeseries"`
	BucketSeconds int64                   `json:"bucketSeconds"`
	GroupBy       string                  `json:"groupBy"`
	Breakdown     []LLMBreakdownRowDTO    `json:"breakdown"`
	// Newest call in the range, Unix ms (0 when there is none).
	LatestCallAt int64 `json:"latestCallAt"`
}

// LLMCallDTO is one call's metadata; message content is never included.
type LLMCallDTO struct {
	// Kind is "slow" or "failed".
	Kind          string   `json:"kind"`
	Timestamp     int64    `json:"timestamp"`
	Service       string   `json:"service"`
	Operation     string   `json:"operation"`
	Model         string   `json:"model"`
	Provider      string   `json:"provider"`
	DurationMs    float64  `json:"durationMs"`
	InputTokens   int64    `json:"inputTokens"`
	OutputTokens  int64    `json:"outputTokens"`
	IsError       bool     `json:"isError"`
	ErrorType     string   `json:"errorType"`
	FinishReasons []string `json:"finishReasons"`
	TraceID       string   `json:"traceId"`
	SpanID        string   `json:"spanId"`
	ServerAddress string   `json:"serverAddress"`
}

type LLMCallsResponseDTO struct {
	Slowest []LLMCallDTO `json:"slowest"`
	Failed  []LLMCallDTO `json:"failed"`
}

func (s *AIUsageService) GetLLMOverview(
	ctx context.Context, projectID uuid.UUID, request *LLMOverviewRequest, user *users_models.User,
) (*LLMOverviewResponseDTO, error) {
	if err := s.authorize(projectID, user, &UsageFilter{From: request.From, To: request.To}); err != nil {
		return nil, err
	}
	if request.GroupBy == "" {
		request.GroupBy = "service"
	}
	groupColumn, ok := llmGroupColumns[request.GroupBy]
	if !ok {
		return nil, fmt.Errorf("%w: groupBy must be service, model, provider or operation", ErrInvalidRequest)
	}
	span := int64(request.To.Sub(request.From) / time.Second)
	if request.BucketSeconds <= 0 {
		request.BucketSeconds = niceBucketSeconds(span / targetBuckets)
	}
	if span/request.BucketSeconds > maxBuckets {
		return nil, fmt.Errorf("%w: bucketSeconds gives more than %d buckets", ErrInvalidRequest, maxBuckets)
	}

	prices, err := loadPrices(projectID)
	if err != nil {
		return nil, err
	}
	rows, err := queryLLM(ctx, projectID, request.From, request.To, s.scanBudget(),
		llmOverviewSQL(groupColumn), request.BucketSeconds)
	if err != nil {
		return nil, err
	}
	return buildLLMOverview(rows, prices, request), nil
}

func (s *AIUsageService) GetLLMCalls(
	ctx context.Context, projectID uuid.UUID, filter *UsageFilter, limit int, user *users_models.User,
) (*LLMCallsResponseDTO, error) {
	if err := s.authorize(projectID, user, filter); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxLLMCalls {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidRequest, maxLLMCalls)
	}
	columns := `epoch_ms(ts::TIMESTAMP)::BIGINT, service, operation, model, provider, duration_ms,
	coalesce(input_tokens, 0)::BIGINT, coalesce(output_tokens, 0)::BIGINT, is_error, error_type,
	finish_reasons, trace_id, span_id, server_address`
	statement := `(SELECT 'slow', ` + columns + ` FROM llm ORDER BY duration_ms DESC LIMIT ?)
UNION ALL
(SELECT 'failed', ` + columns + ` FROM llm WHERE is_error ORDER BY ts DESC LIMIT ?)`
	rows, err := queryLLM(ctx, projectID, filter.From, filter.To, s.scanBudget(),
		statement, limit, limit)
	if err != nil {
		return nil, err
	}

	response := &LLMCallsResponseDTO{Slowest: []LLMCallDTO{}, Failed: []LLMCallDTO{}}
	for _, r := range rows {
		call := LLMCallDTO{
			Kind: toString(r[0]), Timestamp: toInt(r[1]), Service: toString(r[2]), Operation: toString(r[3]),
			Model: toString(r[4]), Provider: toString(r[5]), DurationMs: toFloat(r[6]), InputTokens: toInt(r[7]),
			OutputTokens: toInt(r[8]), IsError: r[9] == true, ErrorType: toString(r[10]),
			FinishReasons: finishReasons(toString(r[11])), TraceID: toString(r[12]), SpanID: toString(r[13]),
			ServerAddress: toString(r[14]),
		}
		if call.Kind == "failed" {
			response.Failed = append(response.Failed, call)
		} else {
			response.Slowest = append(response.Slowest, call)
		}
	}
	sort.SliceStable(response.Slowest, func(i, j int) bool {
		return response.Slowest[i].DurationMs > response.Slowest[j].DurationMs
	})
	sort.SliceStable(response.Failed, func(i, j int) bool {
		return response.Failed[i].Timestamp > response.Failed[j].Timestamp
	})
	return response, nil
}

// llmOverviewSQL is every aggregate of the overview in one pass: the
// total, per group, per group and model (for cost), per bucket and model,
// and per model. groupColumn is from llmGroupColumns.
func llmOverviewSQL(groupColumn string) string {
	return `SELECT
	grouping(bucket)::INTEGER, grouping(grp)::INTEGER, grouping(model)::INTEGER,
	coalesce(epoch_ms(bucket::TIMESTAMP), 0)::BIGINT,
	coalesce(grp, ''),
	coalesce(model, ''),
	count(*)::BIGINT,
	coalesce(sum(input_tokens), 0)::DOUBLE,
	coalesce(sum(output_tokens), 0)::DOUBLE,
	coalesce(sum(cached_tokens), 0)::DOUBLE,
	(count(*) FILTER (WHERE is_error))::BIGINT,
	coalesce(quantile_cont(duration_ms, 0.5), 0)::DOUBLE,
	coalesce(quantile_cont(duration_ms, 0.95), 0)::DOUBLE,
	coalesce(epoch_ms(max(ts)::TIMESTAMP), 0)::BIGINT,
	count(DISTINCT service)::BIGINT,
	(count(*) FILTER (WHERE input_tokens IS NOT NULL OR output_tokens IS NOT NULL))::BIGINT
FROM (SELECT *, time_bucket(to_seconds(?::BIGINT), ts::TIMESTAMP) AS bucket, ` + groupColumn + ` AS grp FROM llm) c
GROUP BY GROUPING SETS ((), (grp), (grp, model), (bucket, model), (model))`
}

type llmAggregate struct {
	key, model                        string
	bucket                            int64
	calls, errors, withTokens, latest int64
	services                          int64
	input, output, cached             float64
	p50, p95                          float64
}

func buildLLMOverview(rows [][]any, prices []*ModelPrice, request *LLMOverviewRequest) *LLMOverviewResponseDTO {
	response := &LLMOverviewResponseDTO{
		BucketSeconds: request.BucketSeconds,
		GroupBy:       request.GroupBy,
		Timeseries:    []LLMTimeseriesPointDTO{},
		Breakdown:     []LLMBreakdownRowDTO{},
		Summary:       LLMSummaryDTO{UnpricedModels: []string{}},
	}
	groups := map[string]*LLMBreakdownRowDTO{}
	for _, r := range rows {
		a := llmAggregate{
			bucket: toInt(r[3]), key: toString(r[4]), model: toString(r[5]), calls: toInt(r[6]),
			input: toFloat(r[7]), output: toFloat(r[8]), cached: toFloat(r[9]), errors: toInt(r[10]),
			p50: toFloat(r[11]), p95: toFloat(r[12]), latest: toInt(r[13]), services: toInt(r[14]),
			withTokens: toInt(r[15]),
		}
		bucketed, grouped, byModel := toInt(r[0]) == 0, toInt(r[1]) == 0, toInt(r[2]) == 0
		switch {
		case bucketed:
			response.Timeseries = append(response.Timeseries, LLMTimeseriesPointDTO{
				Bucket: a.bucket, Model: a.model, Calls: a.calls,
				InputTokens: int64(a.input), OutputTokens: int64(a.output),
			})
		case grouped && byModel:
			row := groupRow(groups, a.key)
			if a.withTokens == 0 {
				continue
			}
			row.HasTokens = true
			if price := matchPrice(prices, a.model); price != nil {
				cost := price.cost(a.input, a.output, a.cached)
				if row.CostUSD == nil {
					row.CostUSD = new(float64)
				}
				*row.CostUSD += cost
			} else {
				row.Unpriced = true
			}
		case grouped:
			row := groupRow(groups, a.key)
			row.Calls, row.InputTokens, row.OutputTokens = a.calls, int64(a.input), int64(a.output)
			row.Errors, row.P95Ms = a.errors, a.p95
		case byModel:
			if a.withTokens == 0 {
				continue
			}
			if price := matchPrice(prices, a.model); price != nil {
				response.Summary.CostUSD += price.cost(a.input, a.output, a.cached)
			} else {
				response.Summary.UnpricedModels = append(response.Summary.UnpricedModels, a.model)
			}
		default:
			response.Summary.Calls, response.Summary.Errors = a.calls, a.errors
			response.Summary.InputTokens, response.Summary.OutputTokens = int64(a.input), int64(a.output)
			response.Summary.CachedTokens, response.Summary.P50Ms, response.Summary.P95Ms = int64(
				a.cached,
			), a.p50, a.p95
			response.Summary.Services, response.LatestCallAt = a.services, a.latest
		}
	}
	for _, row := range groups {
		response.Breakdown = append(response.Breakdown, *row)
	}
	sort.Slice(response.Breakdown, func(i, j int) bool {
		if response.Breakdown[i].Calls != response.Breakdown[j].Calls {
			return response.Breakdown[i].Calls > response.Breakdown[j].Calls
		}
		return response.Breakdown[i].Key < response.Breakdown[j].Key
	})
	if len(response.Breakdown) > maxLLMBreakdownRows {
		response.Breakdown = response.Breakdown[:maxLLMBreakdownRows]
	}
	sort.Strings(response.Summary.UnpricedModels)
	sort.Slice(response.Timeseries, func(i, j int) bool {
		a, b := response.Timeseries[i], response.Timeseries[j]
		if a.Bucket != b.Bucket {
			return a.Bucket < b.Bucket
		}
		return a.Model < b.Model
	})
	return response
}

func groupRow(groups map[string]*LLMBreakdownRowDTO, key string) *LLMBreakdownRowDTO {
	row, ok := groups[key]
	if !ok {
		row = &LLMBreakdownRowDTO{Key: key}
		groups[key] = row
	}
	return row
}

func queryLLM(
	ctx context.Context, projectID uuid.UUID, from, to time.Time, scanBudget int64, statement string, args ...any,
) ([][]any, error) {
	result, err := spans_core.GetSpanCoreRepository().QueryLLMCalls(ctx, projectID, &spans_core.LLMCallsQueryDTO{
		From: from, To: to, SQL: statement, Args: args, ScanBudget: scanBudget,
	})
	if err != nil {
		return nil, err
	}
	return result.Rows, nil
}

// finishReasons reads gen_ai.response.finish_reasons, stored as a JSON
// array or a plain string.
func finishReasons(raw string) []string {
	reasons := []string{}
	if raw == "" {
		return reasons
	}
	if strings.HasPrefix(raw, "[") && json.Unmarshal([]byte(raw), &reasons) == nil {
		return reasons
	}
	return []string{raw}
}
