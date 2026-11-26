package ingest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testRow struct {
	ID       uuid.UUID `json:"id"`
	ClientIP string    `json:"clientIp"`
}

func (r *testRow) RowID() uuid.UUID { return r.ID }

func Test_RowIDs_SameRequest_SameIDsInOrder(t *testing.T) {
	project := uuid.New()
	request := []byte(`[{"message":"a"},{"message":"b"}]`)

	first, second := NewRowIDs(project, request), NewRowIDs(project, request)
	a1, b1 := first.Next(), first.Next()
	a2, b2 := second.Next(), second.Next()

	assert.Equal(t, a1, a2)
	assert.Equal(t, b1, b2)
	assert.NotEqual(t, a1, b1)
	assert.Equal(t, uuid.Version(8), a1.Version())
	assert.Equal(t, uuid.RFC4122, a1.Variant())
}

func Test_RowIDs_OtherRequestOrProject_OtherIDs(t *testing.T) {
	project := uuid.New()
	base := NewRowIDs(project, []byte("one")).Next()

	assert.NotEqual(t, base, NewRowIDs(project, []byte("two")).Next())
	assert.NotEqual(t, base, NewRowIDs(uuid.New(), []byte("one")).Next())
}

func Test_EncodeForPublish_RowsWithSameIDs_SameMessageIDEvenIfOtherFieldsDiffer(t *testing.T) {
	ids := NewRowIDs(uuid.New(), []byte("request"))
	id1, id2 := ids.Next(), ids.Next()

	// A resend through another load balancer node shows another client IP.
	_, first, err := encodeForPublish([]*testRow{{id1, "10.0.0.1"}, {id2, "10.0.0.1"}})
	require.NoError(t, err)
	_, resent, err := encodeForPublish([]*testRow{{id1, "10.0.0.2"}, {id2, "10.0.0.2"}})
	require.NoError(t, err)
	_, other, err := encodeForPublish([]*testRow{{id2, "10.0.0.1"}})
	require.NoError(t, err)

	require.Len(t, first, 1)
	assert.Equal(t, first, resent)
	assert.NotEqual(t, first, other)
}
