package report

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// AdvicePath is where a Site receives cost advice batches, next to the cost batches.
const AdvicePath = "/v1/cloud/advice"

// MaxAdviceRecommendationsPerChunk keeps each request well inside the Site's body limit.
const MaxAdviceRecommendationsPerChunk = 5000

// SendAdvice sends one advice refresh to the destination's Site in chunks; the Site applies it
// once every chunk has arrived. Sections, commitments, coverage and options go with the first.
func (s *Sender) SendAdvice(ctx context.Context, destination spec.Destination, batch spec.AdviceBatch) error {
	batch.BatchID = uuid.NewString()
	recs := batch.Recommendations
	chunks := max(1, (len(recs)+MaxAdviceRecommendationsPerChunk-1)/MaxAdviceRecommendationsPerChunk)
	chunks = min(chunks, spec.MaxAdviceChunks)
	url := strings.TrimRight(destination.OTLPURL, "/") + AdvicePath
	for i := range chunks {
		chunk := batch
		chunk.Seq, chunk.Total = i+1, chunks
		chunk.Recommendations = recs[min(len(recs), i*MaxAdviceRecommendationsPerChunk):min(len(recs),
			(i+1)*MaxAdviceRecommendationsPerChunk)]
		if i > 0 {
			chunk.Sections, chunk.Commitments, chunk.Coverage, chunk.Options = nil, nil, nil, nil
		}
		if err := s.postJSON(ctx, url, destination.APIKey, chunk); err != nil {
			return err
		}
	}
	return nil
}
