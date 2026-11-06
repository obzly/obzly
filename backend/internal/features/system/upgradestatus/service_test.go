package system_upgradestatus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/features/datamigration"
	system_integrity "obzly/internal/features/system/integrity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_BuildVerdict_WhenNothingOutstanding_SafeToUpgrade(t *testing.T) {
	zero := int64(0)
	verdict := BuildVerdict(&Response{
		Schema:               SchemaStatus{State: SchemaUpToDate},
		Rewrites:             &RewriteStatus{Streams: []datamigration.StreamStatus{{Stream: "logs"}}},
		Segments:             []StreamBacklog{{Stream: "logs", PendingUpload: &zero}},
		ObjectStorageEnabled: true,
	})

	assert.True(t, verdict.SafeToUpgrade)
	assert.Empty(t, verdict.Blockers)
	assert.Empty(t, verdict.Warnings)
}

func Test_BuildVerdict_WhenRewritesRemain_Blocks(t *testing.T) {
	verdict := BuildVerdict(&Response{
		Schema:   SchemaStatus{State: SchemaUpToDate},
		Rewrites: &RewriteStatus{Remaining: 12},
	})

	assert.False(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Blockers, 1)
	assert.Contains(t, verdict.Blockers[0], "12 segments still being rewritten")
}

func Test_BuildVerdict_WhenOnlySkippedSegments_WarnsButAllows(t *testing.T) {
	verdict := BuildVerdict(&Response{
		Schema:   SchemaStatus{State: SchemaUpToDate},
		Rewrites: &RewriteStatus{Streams: []datamigration.StreamStatus{{Stream: "logs", Legacy: 2, Skipped: 2}}},
	})

	assert.True(t, verdict.SafeToUpgrade)
	assert.Empty(t, verdict.Blockers)
	require.Len(t, verdict.Warnings, 1)
	assert.Contains(t, verdict.Warnings[0], "2 segments couldn't be rewritten")
}

func Test_BuildVerdict_WhenUploadsPendingWithObjectStorage_Blocks(t *testing.T) {
	three := int64(3)
	verdict := BuildVerdict(&Response{
		Schema:               SchemaStatus{State: SchemaUpToDate},
		Segments:             []StreamBacklog{{Stream: "spans", PendingUpload: &three}},
		ObjectStorageEnabled: true,
	})

	assert.False(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Blockers, 1)
	assert.Contains(t, verdict.Blockers[0], "3 segments not yet uploaded")
}

func Test_BuildVerdict_WhenSchemaAhead_Blocks(t *testing.T) {
	verdict := BuildVerdict(&Response{
		Schema: SchemaStatus{State: SchemaAhead, CurrentVersion: 30, LatestAvailable: 20},
	})

	assert.False(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Blockers, 1)
	assert.Contains(t, verdict.Blockers[0], "rollback")
}

func Test_LatestShippedMigration_PicksHighestVersionAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"20260101000000_first.sql", "20260925090000_later.sql", "README.md", "notes_20270101000000.sql",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(""), 0o600))
	}

	latest, err := latestShippedMigration(dir)

	require.NoError(t, err)
	assert.Equal(t, int64(20260925090000), latest)
}

func Test_LatestShippedMigration_WhenDirMissing_ReturnsError(t *testing.T) {
	_, err := latestShippedMigration(filepath.Join(t.TempDir(), "missing"))

	assert.Error(t, err)
}

func Test_BuildVerdict_WhenOneReleaseAhead_WarnsButAllows(t *testing.T) {
	verdict := BuildVerdict(&Response{
		Schema: SchemaStatus{State: SchemaAhead, CurrentVersion: 30, LatestAvailable: 20, ReleasesAhead: 1},
	})

	assert.True(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Warnings, 1)
	assert.Contains(t, verdict.Warnings[0], "one release newer")
}

func Test_BuildVerdict_WhenTwoReleasesAhead_Blocks(t *testing.T) {
	verdict := BuildVerdict(&Response{
		Schema: SchemaStatus{State: SchemaAhead, CurrentVersion: 40, LatestAvailable: 20, ReleasesAhead: 2},
	})

	assert.False(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Blockers, 1)
	assert.Contains(t, verdict.Blockers[0], "one-version boundary")
}

func Test_BuildVerdict_WhenIngestBufferBehind_WarnsButAllows(t *testing.T) {
	now := time.Now().UTC()
	old, fresh := now.Add(-time.Hour), now.Add(-time.Minute)
	verdict := BuildVerdict(&Response{
		CheckedAt: now,
		IngestBuffer: []IngestStreamStatus{
			{Stream: "INGEST_LOGS", Messages: 10, OldestAt: &old},
			{Stream: "INGEST_SPANS", Messages: 10, OldestAt: &fresh, ReplicasLagging: 1},
			{Stream: "INGEST_METRICS"},
		},
	})

	assert.True(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Warnings, 2)
	assert.Contains(t, verdict.Warnings[0], "INGEST_LOGS")
	assert.Contains(t, verdict.Warnings[1], "broker replica")
}

func Test_BuildVerdict_WhenIntegrityAuditFailed_WarnsButAllows(t *testing.T) {
	verdict := BuildVerdict(&Response{
		IntegrityAudit: &system_integrity.LastAudit{FinishedAt: time.Now().UTC(), Sampled: 50, Failed: 2},
	})

	assert.True(t, verdict.SafeToUpgrade)
	require.Len(t, verdict.Warnings, 1)
	assert.Contains(t, verdict.Warnings[0], "2 of 50")
	assert.Contains(t, verdict.Warnings[0], "obzly reconcile")
}
