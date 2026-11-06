// Package system_metrics backs the admin System Dashboard: host resource usage plus an
// ingestion-rate counter. Segments flush every few thousand rows or up to 10 minutes, so
// rates derived from them would lag; instead each accepted batch is counted in
// per-minute Valkey buckets and the dashboard sums the last few.
package system_metrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"obzly/internal/cache"

	"github.com/valkey-io/valkey-go"
)

const (
	ingestionRateKeyPrefix = "ingest_rate:"
	// Kept longer than any reported window, so a request at a minute's edge still sees
	// the bucket.
	bucketTTLSeconds = 20 * 60
	doTimeout        = 5 * time.Second
)

// RecordIngestion adds one accepted batch's counts to the current UTC minute's bucket.
// Called per batch, not per line, so Valkey cost doesn't grow with batch size.
func RecordIngestion(logCount int, rawBytes int64) {
	if logCount <= 0 {
		return
	}

	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(context.Background(), doTimeout)
	defer cancel()

	bucket := currentMinuteBucket()
	countKey := ingestionRateKeyPrefix + "count:" + bucket
	bytesKey := ingestionRateKeyPrefix + "bytes:" + bucket

	cmds := []valkey.Completed{
		client.B().Incrby().Key(countKey).Increment(int64(logCount)).Build(),
		client.B().Expire().Key(countKey).Seconds(bucketTTLSeconds).Build(),
		client.B().Incrby().Key(bytesKey).Increment(rawBytes).Build(),
		client.B().Expire().Key(bytesKey).Seconds(bucketTTLSeconds).Build(),
	}
	client.DoMulti(ctx, cmds...)
}

// IngestionRate summarizes accepted ingestion over the trailing window.
type IngestionRate struct {
	WindowMinutes  int
	TotalCount     int64
	TotalRawBytes  int64
	AvgCountPerMin float64
}

// GetIngestionRateLastNMinutes sums the last n per-minute buckets, including the current
// one. Minutes without traffic have no key (MGET nil = zero).
func GetIngestionRateLastNMinutes(n int) (*IngestionRate, error) {
	if n <= 0 {
		n = 15
	}

	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(context.Background(), doTimeout)
	defer cancel()

	now := time.Now().UTC()
	countKeys := make([]string, n)
	bytesKeys := make([]string, n)
	for i := range n {
		bucket := minuteBucket(now.Add(-time.Duration(i) * time.Minute))
		countKeys[i] = ingestionRateKeyPrefix + "count:" + bucket
		bytesKeys[i] = ingestionRateKeyPrefix + "bytes:" + bucket
	}

	countTotal, err := sumKeys(ctx, client, countKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to read ingestion count buckets: %w", err)
	}
	bytesTotal, err := sumKeys(ctx, client, bytesKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to read ingestion byte buckets: %w", err)
	}

	return &IngestionRate{
		WindowMinutes:  n,
		TotalCount:     countTotal,
		TotalRawBytes:  bytesTotal,
		AvgCountPerMin: float64(countTotal) / float64(n),
	}, nil
}

func sumKeys(ctx context.Context, client valkey.Client, keys []string) (int64, error) {
	result := client.Do(ctx, client.B().Mget().Key(keys...).Build())
	if result.Error() != nil {
		return 0, result.Error()
	}

	values, err := result.ToArray()
	if err != nil {
		return 0, err
	}

	var total int64
	for _, value := range values {
		if value.IsNil() {
			continue
		}
		str, err := value.ToString()
		if err != nil {
			continue
		}
		parsed, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			continue
		}
		total += parsed
	}

	return total, nil
}

func currentMinuteBucket() string {
	return minuteBucket(time.Now().UTC())
}

func minuteBucket(t time.Time) string {
	return strconv.FormatInt(t.Unix()/60, 10)
}
