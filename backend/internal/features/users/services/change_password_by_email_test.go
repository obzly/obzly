package users_services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GetUserByEmail returns (nil, nil) for an unknown email; the CLI reset flow
// must report that, not panic.
func Test_ChangeUserPasswordByEmail_UnknownEmail_ReturnsErrorNotPanic(t *testing.T) {
	err := GetUserService().ChangeUserPasswordByEmail(
		"no-such-user-"+uuid.New().String()+"@example.com", "newpassword123",
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no user found")
}
