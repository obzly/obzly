package cache_utils

import (
	"context"
	"obzly/internal/cache"
	"time"

	"github.com/valkey-io/valkey-go"
)

type ValkeyQueueService struct {
	client  valkey.Client
	timeout time.Duration
}

func NewValkeyQueueService() *ValkeyQueueService {
	return &ValkeyQueueService{
		client:  cache.GetCache(),
		timeout: DefaultQueueTimeout,
	}
}

func (q *ValkeyQueueService) EnqueueBatch(queueKey string, items [][]byte) error {
	if len(items) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmds := make([]valkey.Completed, 0, len(items))

	for _, item := range items {
		cmd := q.client.B().Lpush().Key(queueKey).Element(string(item)).Build()
		cmds = append(cmds, cmd)
	}

	results := q.client.DoMulti(ctx, cmds...)

	for _, result := range results {
		if result.Error() != nil {
			return result.Error()
		}
	}

	return nil
}

// DequeueBatch pops up to maxCount items, oldest first, with one atomic RPOP ... COUNT.
// Pipelined single RPOPs lost items when an EnqueueBatch landed mid-pipeline.
func (q *ValkeyQueueService) DequeueBatch(queueKey string, maxCount int, timeout time.Duration) ([][]byte, error) {
	if maxCount <= 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	response := q.client.Do(ctx, q.client.B().Rpop().Key(queueKey).Count(int64(maxCount)).Build())
	if err := response.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, err
	}

	items, err := response.AsStrSlice()
	if err != nil {
		return nil, err
	}

	results := make([][]byte, 0, len(items))
	for _, item := range items {
		results = append(results, []byte(item))
	}

	return results, nil
}

func (q *ValkeyQueueService) DequeueBlocking(queueKey string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmd := q.client.B().Brpop().Key(queueKey).Timeout(timeout.Seconds()).Build()
	result := q.client.Do(ctx, cmd)

	if result.Error() != nil {
		return nil, result.Error()
	}

	// BRPOP returns [key, value], we want the value
	arr, err := result.AsStrSlice()
	if err != nil {
		return nil, err
	}

	if len(arr) < 2 {
		return nil, valkey.Nil
	}

	return []byte(arr[1]), nil
}

func (q *ValkeyQueueService) QueueLength(queueKey string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmd := q.client.B().Llen().Key(queueKey).Build()
	result := q.client.Do(ctx, cmd)

	if result.Error() != nil {
		return 0, result.Error()
	}

	return result.AsInt64()
}

func (q *ValkeyQueueService) ClearQueue(queueKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	cmd := q.client.B().Del().Key(queueKey).Build()
	result := q.client.Do(ctx, cmd)

	return result.Error()
}
