package ingest

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/google/uuid"
)

// RowIDs gives a request's rows ids derived from the request itself, so a
// client resending a batch the broker had already stored (its ack was lost
// or late) produces the same rows. Their payloads then carry the same
// Nats-Msg-Id, which JetStream drops inside its duplicate window, and the
// same ids, which compaction keeps once after that.
type RowIDs struct {
	base [16]byte
	next uint64
}

// NewRowIDs seeds ids from projectID and the request's canonical bytes
// (deterministic protobuf, or JSON of the decoded request).
func NewRowIDs(projectID uuid.UUID, request []byte) *RowIDs {
	h := sha256.New()
	h.Write(projectID[:])
	h.Write(request)
	ids := &RowIDs{}
	copy(ids.base[:], h.Sum(nil))
	return ids
}

// Next is the id of the request's next row: the digest plus the row's
// index, as a version 8 UUID. Distinct within a request; across requests
// as unlikely to collide as random ids.
func (r *RowIDs) Next() uuid.UUID {
	id := uuid.UUID(r.base)
	low := binary.BigEndian.Uint64(id[8:]) + r.next
	r.next++
	binary.BigEndian.PutUint64(id[8:], low)
	id[6] = id[6]&0x0f | 0x80 // version 8
	id[8] = id[8]&0x3f | 0x80 // RFC 9562 variant
	return id
}
