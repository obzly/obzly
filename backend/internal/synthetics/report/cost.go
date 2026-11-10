package report

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// CostPath is where a Site receives cost batches, next to the OTLP endpoints.
const CostPath = "/v1/cloud/cost"

// MaxCostRowsPerChunk keeps each request well inside the Site's body limit.
const MaxCostRowsPerChunk = 20000

// SendCost sends one batch to the destination's Site in chunks; the Site applies the batch once
// every chunk has arrived. A batch with no rows is still sent: it clears the days it covers.
func (s *Sender) SendCost(ctx context.Context, destination spec.Destination, batch spec.CostBatch) error {
	batch.BatchID = uuid.NewString()
	chunks := max(1, (len(batch.Rows)+MaxCostRowsPerChunk-1)/MaxCostRowsPerChunk)
	rows, forecast := batch.Rows, batch.Forecast
	url := strings.TrimRight(destination.OTLPURL, "/") + CostPath
	for i := range chunks {
		chunk := batch
		chunk.Seq, chunk.Total = i+1, chunks
		chunk.Rows = rows[i*MaxCostRowsPerChunk : min(len(rows), (i+1)*MaxCostRowsPerChunk)]
		chunk.Forecast = nil
		if i == chunks-1 {
			chunk.Forecast = forecast
		}
		if err := s.postJSON(ctx, url, destination.APIKey, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sender) postJSON(ctx context.Context, url, apiKey string, body any) error {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &compressed)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("X-API-Key", apiKey)
	response, err := s.Client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode != http.StatusOK {
		return &StatusError{URL: url, Status: response.StatusCode, Body: strings.TrimSpace(string(answer))}
	}
	return nil
}
