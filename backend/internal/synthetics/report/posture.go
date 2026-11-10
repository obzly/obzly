package report

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"obzly/internal/synthetics/spec"
)

// PosturePath is where a Site receives posture batches, next to the cost batches.
const PosturePath = "/v1/cloud/posture"

// MaxPostureFindingsPerChunk keeps each request well inside the Site's body limit.
const MaxPostureFindingsPerChunk = 5000

// SendPosture sends one refresh to the destination's Site in chunks; the Site applies it once
// every chunk has arrived. The runs go with the first chunk.
func (s *Sender) SendPosture(ctx context.Context, destination spec.Destination, batch spec.PostureBatch) error {
	batch.BatchID = uuid.NewString()
	findings := batch.Findings
	chunks := max(1, (len(findings)+MaxPostureFindingsPerChunk-1)/MaxPostureFindingsPerChunk)
	url := strings.TrimRight(destination.OTLPURL, "/") + PosturePath
	for i := range chunks {
		chunk := batch
		chunk.Seq, chunk.Total = i+1, chunks
		chunk.Findings = findings[i*MaxPostureFindingsPerChunk : min(len(findings), (i+1)*MaxPostureFindingsPerChunk)]
		if i > 0 {
			chunk.Runs = nil
		}
		if err := s.postJSON(ctx, url, destination.APIKey, chunk); err != nil {
			return err
		}
	}
	return nil
}
