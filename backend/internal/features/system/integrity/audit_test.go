package system_integrity

import (
	"context"
	"testing"

	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "gocloud.dev/blob/memblob"
)

// Run records each audit; Latest (Upgrade status, the metrics on every
// replica) reads the newest back.
func Test_Run_RecordsAuditForLatest(t *testing.T) {
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	project := uuid.New() // no segments: an empty, clean audit

	audit, err := Run(context.Background(), store, []uuid.UUID{project})
	require.NoError(t, err)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM integrity_audits WHERE finished_at = ?", audit.FinishedAt) })
	assert.Zero(t, audit.Sampled)

	latest, err := Latest()
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.WithinDuration(t, audit.FinishedAt, latest.FinishedAt, 0)
	assert.Empty(t, latest.Failures)
}
