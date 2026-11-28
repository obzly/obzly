package query

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// A deadline hit in this process or on an executor (its error comes back as
// text) is a timeout; any other failure isn't.
func Test_IsTimeout_DeadlineHereOrOnAnExecutor_IsATimeout(t *testing.T) {
	local := fmt.Errorf("query fragment failed: %w", fmt.Errorf("fragment f1: %w", context.DeadlineExceeded))
	remote := errors.New("query fragment failed: fragment f1: context deadline exceeded")

	require.True(t, IsTimeout(local))
	require.True(t, IsTimeout(remote))
	require.False(t, IsTimeout(errors.New("query fragment failed: no such column")))
}
