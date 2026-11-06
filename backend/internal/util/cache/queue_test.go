package cache_utils

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_DequeueBatch_WhileEnqueueing_LosesNothing(t *testing.T) {
	queue := NewValkeyQueueService()
	queueKey := "test_queue:" + uuid.NewString()
	defer func() { _ = queue.ClearQueue(queueKey) }()

	const batches, perBatch = 200, 50
	var producers sync.WaitGroup
	producers.Add(1)
	go func() {
		defer producers.Done()
		for b := range batches {
			items := make([][]byte, perBatch)
			for i := range items {
				items[i] = []byte(fmt.Sprintf("%d-%d", b, i))
			}
			if err := queue.EnqueueBatch(queueKey, items); err != nil {
				t.Errorf("enqueue failed: %v", err)
				return
			}
		}
	}()

	received := map[string]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for len(received) < batches*perBatch && time.Now().Before(deadline) {
		items, err := queue.DequeueBatch(queueKey, 5000, 0)
		require.NoError(t, err)
		for _, item := range items {
			received[string(item)] = true
		}
	}
	producers.Wait()

	assert.Len(t, received, batches*perBatch)
}
