package segments

import (
	"context"
	"sync"

	"obzly/internal/telemetry"
)

// warmConcurrency: downloads WarmCache runs at once.
const warmConcurrency = 8

// WarmResult is what a WarmCache pass did.
type WarmResult struct {
	Downloaded int
	Cached     int
	Failed     int
	Bytes      int64
}

// WarmCache puts records (newest first) into the cold cache at cacheDir,
// up to maxBytes of them counting those already there, so the first queries
// after a restart read local files. A failed download is counted and
// skipped: a query fetches it later as usual.
func WarmCache(
	ctx context.Context, store *ObjectStore, cacheDir string, records []*SegmentRecord, maxBytes int64,
) WarmResult {
	var result WarmResult
	var mu sync.Mutex
	slots := make(chan struct{}, warmConcurrency)
	var wg sync.WaitGroup
	var planned int64
	for _, record := range records {
		if planned+record.SizeBytes > maxBytes || ctx.Err() != nil {
			break
		}
		planned += record.SizeBytes
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			_, source, err := resolveFile(ctx, store, cacheDir, record)
			FetchTokenBloom(ctx, store, cacheDir, record)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				result.Failed++
			case source == telemetry.SourceObjectStore:
				result.Downloaded++
			default:
				result.Cached++
			}
		})
	}
	wg.Wait()
	result.Bytes = planned
	return result
}
