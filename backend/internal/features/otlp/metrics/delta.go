package otlp_metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

const (
	deltaKeyPrefix = "otlp_delta:"
	// deltaTotalTTL forgets a series' running total a day after its last
	// point; a total that comes back starts from zero, which PromQL reads
	// as a counter reset.
	deltaTotalTTL = 24 * time.Hour
)

// DeltaTotals keeps a running total per series, shared by every api replica.
type DeltaTotals interface {
	// Add adds each increment to its key's total, in order, and returns the
	// total after each one.
	Add(ctx context.Context, increments []DeltaIncrement) ([]float64, error)
}

type DeltaIncrement struct {
	Key   string
	Value float64
}

type valkeyDeltaTotals struct {
	client valkey.Client
}

func NewValkeyDeltaTotals(client valkey.Client) DeltaTotals {
	return &valkeyDeltaTotals{client}
}

// Add sends every INCRBYFLOAT plus one EXPIRE per key in a single pipeline,
// so a request costs one round trip however many points it carries.
func (v *valkeyDeltaTotals) Add(ctx context.Context, increments []DeltaIncrement) ([]float64, error) {
	commands := make(valkey.Commands, 0, len(increments)*2)
	expiring := make(map[string]bool, len(increments))
	for _, increment := range increments {
		key := deltaKeyPrefix + increment.Key
		commands = append(commands, v.client.B().Incrbyfloat().Key(key).Increment(increment.Value).Build())
	}
	for _, increment := range increments {
		key := deltaKeyPrefix + increment.Key
		if expiring[key] {
			continue
		}
		expiring[key] = true
		commands = append(commands, v.client.B().Expire().Key(key).Seconds(int64(deltaTotalTTL.Seconds())).Build())
	}

	results := v.client.DoMulti(ctx, commands...)
	totals := make([]float64, len(increments))
	for i := range increments {
		total, err := results[i].AsFloat64()
		if err != nil {
			return nil, fmt.Errorf("failed to add a delta to its running total: %w", err)
		}
		totals[i] = total
	}
	for _, result := range results[len(increments):] {
		if err := result.Error(); err != nil {
			return nil, fmt.Errorf("failed to refresh a running total's TTL: %w", err)
		}
	}
	return totals, nil
}

// SetDeltaTotalsForTest swaps the running-total store, so a test can make it
// fail like an unreachable Valkey; the returned func restores it.
func SetDeltaTotalsForTest(totals DeltaTotals) (restore func()) {
	previous := otlpMetricsService.deltaTotals
	otlpMetricsService.deltaTotals = totals
	return func() { otlpMetricsService.deltaTotals = previous }
}

// toCumulative replaces each delta sample's Value with its series' running
// total. A series' points are added oldest first; across requests they are
// added in arrival order, so a late point adds to the current total.
// It returns the increments that undo it, for a request whose samples
// then fail to store.
func toCumulative(
	ctx context.Context,
	totals DeltaTotals,
	projectID uuid.UUID,
	deltas []*metrics_core.MetricSample,
) ([]DeltaIncrement, error) {
	var keys []string
	bySeries := map[string][]*metrics_core.MetricSample{}
	for _, sample := range deltas {
		key := deltaSeriesKey(projectID, sample)
		if _, seen := bySeries[key]; !seen {
			keys = append(keys, key)
		}
		bySeries[key] = append(bySeries[key], sample)
	}

	increments := make([]DeltaIncrement, 0, len(deltas))
	ordered := make([]*metrics_core.MetricSample, 0, len(deltas))
	for _, key := range keys {
		points := bySeries[key]
		sort.SliceStable(points, func(i, j int) bool { return points[i].Timestamp.Before(points[j].Timestamp) })
		for _, point := range points {
			increments = append(increments, DeltaIncrement{key, point.Value})
			ordered = append(ordered, point)
		}
	}

	values, err := totals.Add(ctx, increments)
	if err != nil {
		return nil, err
	}
	undo := make([]DeltaIncrement, len(increments))
	for i, sample := range ordered {
		sample.Value = values[i]
		undo[i] = DeltaIncrement{increments[i].Key, -increments[i].Value}
	}
	return undo, nil
}

// deltaSeriesKey is the project plus the stored series' id (metric name and
// key-sorted labels, as the metrics stream stores them).
func deltaSeriesKey(projectID uuid.UUID, sample *metrics_core.MetricSample) string {
	labelsJSON := "{}"
	if len(sample.Labels) > 0 {
		// A map[string]string always marshals, with its keys sorted.
		encoded, _ := json.Marshal(sample.Labels)
		labelsJSON = string(encoded)
	}
	return projectID.String() + ":" + strconv.FormatUint(segments.SeriesID(sample.MetricName, labelsJSON), 16)
}
