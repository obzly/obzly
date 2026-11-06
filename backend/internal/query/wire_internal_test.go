package query

import (
	"testing"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Arguments keep their type across the wire: a time must bind as a
// timestamp, not a string.
func Test_Fragment_RoundTripsThroughTheWire(t *testing.T) {
	at := time.Date(2026, 10, 2, 8, 30, 0, 123456789, time.UTC)
	fragment := Fragment{
		ID: "f1", Stream: "logs", SQL: "SELECT 1",
		Segments: []*segments.SegmentRecord{{ID: uuid.New(), ObjectKey: "k", Stats: &segments.SegmentStats{}}},
		Args:     []any{"text", int64(7), 3, 1.5, true, at, nil},
	}

	data, err := encodeFragment(fragment)
	require.NoError(t, err)
	decoded, err := decodeFragment(data)
	require.NoError(t, err)

	require.Equal(t, []any{"text", int64(7), int64(3), 1.5, true, at, nil}, decoded.Args)
	require.Equal(t, fragment.Segments[0].ID, decoded.Segments[0].ID)
	require.Nil(t, decoded.Segments[0].Stats, "stats stay behind")
}

func Test_EncodeFragment_WithLocalFiles_IsRejected(t *testing.T) {
	_, err := encodeFragment(Fragment{ID: "f1", LocalFiles: []segments.ResolvedFile{{Path: "/tail"}}})

	require.ErrorContains(t, err, "cannot run remotely")
}
