package aiusage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NiceBucketSeconds_RoundsUpToClockFriendlySizes(t *testing.T) {
	assert.Equal(t, int64(60), niceBucketSeconds(1))
	assert.Equal(t, int64(300), niceBucketSeconds(61))
	assert.Equal(t, int64(1800), niceBucketSeconds(1440))
	assert.Equal(t, int64(86400), niceBucketSeconds(50000))
	assert.Equal(t, int64(2*86400), niceBucketSeconds(90000))
}
