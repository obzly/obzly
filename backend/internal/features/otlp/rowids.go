package otlp

import (
	"obzly/internal/ingest"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// RowIDs seeds a request's row ids from its deterministic protobuf encoding,
// so a resent batch stores the same rows (ingest.RowIDs). A request that
// can't be encoded gets random ids.
func RowIDs(projectID uuid.UUID, request proto.Message) *ingest.RowIDs {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		random := uuid.New()
		encoded = random[:]
	}
	return ingest.NewRowIDs(projectID, encoded)
}
