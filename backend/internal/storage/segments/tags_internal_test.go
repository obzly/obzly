package segments

import (
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Uploads are tagged with stream, project and level from their key, for
// tag-filtered S3 lifecycle rules.
func Test_WriterOptions_TagsTheS3UploadFromItsKey(t *testing.T) {
	record := &SegmentRecord{
		ID: uuid.New(), ProjectID: uuid.New(), Stream: "spans", Level: LevelCold,
		MaxTime: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
	key := ObjectKeyFor(record)
	options := writerOptions(key)
	require.NotNil(t, options)

	input := &transfermanager.UploadObjectInput{}
	require.NoError(t, options.BeforeWrite(func(target any) bool {
		if p, ok := target.(**transfermanager.UploadObjectInput); ok {
			*p = input
			return true
		}
		return false
	}))
	require.NotNil(t, input.Tagging)
	tags, err := url.ParseQuery(*input.Tagging)
	require.NoError(t, err)
	require.Equal(t, "spans", tags.Get("obzly-stream"))
	require.Equal(t, record.ProjectID.String(), tags.Get("obzly-project"))
	require.Equal(t, "2", tags.Get("obzly-level"))

	// Another store's writer has no S3 input: nothing to tag, no error.
	require.NoError(t, options.BeforeWrite(func(any) bool { return false }))
	require.Nil(t, writerOptions("legacy/"+uuid.NewString()+".parquet"), "other layouts aren't tagged")
}
