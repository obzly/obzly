package logs_querier_test

import (
	"context"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A streamed histogram arrives in several emits, newest partitions first,
// that add up to exactly the one-shot answer.
func Test_StreamAggregation_EmitsNewestFirst_AddingUpToTheWholeAnswer(t *testing.T) {
	f := seedRollupFixture(t)
	from, to := f.base, f.base.Add(2*time.Hour-time.Nanosecond)
	request := aggregationRequest(nil, from, to)
	request.GroupField, request.BucketMs, request.Limit = "level", time.Minute.Milliseconds(), 10_000
	whole, err := f.querier.ExecuteAggregation(context.Background(), f.projectID, request)
	require.NoError(t, err)

	type key struct {
		at    time.Time
		group string
	}
	sums := map[key]int64{}
	var newestPerEmit []time.Time
	response, err := f.querier.StreamAggregation(context.Background(), f.projectID, request,
		func(buckets []logs_core.AggregationBucketDTO) error {
			var newest time.Time
			for _, bucket := range buckets {
				sums[key{*bucket.Timestamp, bucket.GroupKey}] += bucket.Count
				if bucket.Timestamp.After(newest) {
					newest = *bucket.Timestamp
				}
			}
			newestPerEmit = append(newestPerEmit, newest)
			return nil
		})
	require.NoError(t, err)

	require.Greater(t, len(newestPerEmit), 1, "partition groups arrive one by one")
	for i := 1; i < len(newestPerEmit); i++ {
		assert.True(t, newestPerEmit[i].Before(newestPerEmit[i-1]), "newest first")
	}
	assert.Equal(t, whole.Buckets, response.Buckets)
	require.Len(t, sums, len(whole.Buckets))
	for _, bucket := range whole.Buckets {
		assert.Equal(t, bucket.Count, sums[key{*bucket.Timestamp, bucket.GroupKey}])
	}
}

// A request that isn't time-bucketed is one emit of the whole answer.
func Test_StreamAggregation_Unbucketed_EmitsOnce(t *testing.T) {
	f := seedRollupFixture(t)
	request := aggregationRequest(nil, f.base, f.base.Add(2*time.Hour))
	request.GroupField = "service_name"
	var emits [][]logs_core.AggregationBucketDTO
	response, err := f.querier.StreamAggregation(context.Background(), f.projectID, request,
		func(buckets []logs_core.AggregationBucketDTO) error {
			emits = append(emits, buckets)
			return nil
		})
	require.NoError(t, err)
	require.Len(t, emits, 1)
	assert.Equal(t, response.Buckets, emits[0])
}
