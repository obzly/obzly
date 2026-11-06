package system_usage

import (
	"sort"
	"sync"
	"time"

	"obzly/internal/storage"
)

// Segments are summed by the hour of their newest row: a compacted segment
// counts in its last hour, so hours are close, not exact.
const (
	window   = 24 * time.Hour
	cacheFor = time.Minute
)

// signals maps segment streams to what Settings shows; streams derived
// from others (logs_1m) aren't data that arrived.
var signals = map[string]string{
	"logs":           "logs",
	"ai_logs":        "logs",
	"metrics":        "metrics",
	"metrics_series": "metrics",
	"spans":          "traces",
}

type Service struct {
	mu       sync.Mutex
	cached   *Response
	cachedAt time.Time
}

func (s *Service) GetUsage() (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Since(s.cachedAt) < cacheFor {
		return s.cached, nil
	}
	response, err := read(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	s.cached, s.cachedAt = response, time.Now().UTC()
	return response, nil
}

func read(now time.Time) (*Response, error) {
	var hourRows []struct {
		Stream   string
		Hour     time.Time
		RawBytes int64
		Rows     int64
	}
	if err := storage.GetDb().Raw(`SELECT stream, date_trunc('hour', max_time) AS hour,
			SUM(raw_size_bytes) AS raw_bytes, SUM(row_count) AS rows
		FROM stream_segments WHERE max_time >= ? GROUP BY stream, hour`, now.Add(-window)).
		Scan(&hourRows).Error; err != nil {
		return nil, err
	}
	var storageRows []struct {
		Stream   string
		Bytes    int64
		RawBytes int64
		Oldest   *time.Time
	}
	if err := storage.GetDb().Raw(`SELECT stream, SUM(size_bytes) AS bytes, SUM(raw_size_bytes) AS raw_bytes,
			MIN(min_time) AS oldest
		FROM stream_segments GROUP BY stream`).Scan(&storageRows).Error; err != nil {
		return nil, err
	}

	hours := map[[2]string]*Hour{}
	for _, row := range hourRows {
		signal, ok := signals[row.Stream]
		if !ok {
			continue
		}
		key := [2]string{row.Hour.UTC().Format(time.RFC3339), signal}
		if hours[key] == nil {
			hours[key] = &Hour{Hour: row.Hour.UTC(), Signal: signal}
		}
		hours[key].RawBytes += row.RawBytes
		hours[key].Rows += row.Rows
	}
	stored := map[string]*Storage{}
	for _, row := range storageRows {
		signal, ok := signals[row.Stream]
		if !ok {
			continue
		}
		if stored[signal] == nil {
			stored[signal] = &Storage{Signal: signal}
		}
		item := stored[signal]
		item.Bytes += row.Bytes
		item.RawBytes += row.RawBytes
		if row.Oldest != nil && (item.Oldest == nil || row.Oldest.Before(*item.Oldest)) {
			oldest := row.Oldest.UTC()
			item.Oldest = &oldest
		}
	}

	response := &Response{Hours: []Hour{}, Storage: []Storage{}}
	for _, hour := range hours {
		response.Hours = append(response.Hours, *hour)
	}
	sort.Slice(response.Hours, func(i, j int) bool {
		a, b := response.Hours[i], response.Hours[j]
		return a.Hour.Before(b.Hour) || (a.Hour.Equal(b.Hour) && a.Signal < b.Signal)
	})
	for _, item := range stored {
		response.Storage = append(response.Storage, *item)
	}
	sort.Slice(response.Storage, func(i, j int) bool { return response.Storage[i].Signal < response.Storage[j].Signal })
	return response, nil
}
