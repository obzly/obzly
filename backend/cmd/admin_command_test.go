package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	users_enums "obzly/internal/features/users/enums"
	users_repositories "obzly/internal/features/users/repositories"
	users_testing "obzly/internal/features/users/testing"
)

func Test_ParseAdminCommand_OnlyTheAdminFlagsLeaveServerStartup(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		ok      bool
		want    adminCommand
		wantErr bool
	}{
		{name: "no arguments start the server", args: nil},
		{name: "other arguments start the server", args: []string{"serve"}},
		{name: "email prompts for a password", args: []string{"--email=admin"}, ok: true,
			want: adminCommand{Email: "admin"}},
		{name: "single dash and a separate value", args: []string{"-email", "admin", "--reset-mfa"}, ok: true,
			want: adminCommand{Email: "admin", ResetMFA: true}},
		{name: "both resets", args: []string{"--email=a@example.com", "--reset-mfa", "--new-password=x"}, ok: true,
			want: adminCommand{Email: "a@example.com", ResetMFA: true, NewPassword: "x"}},
		{name: "bootstrap token alone", args: []string{"--reset-bootstrap-token"}, ok: true,
			want: adminCommand{ResetBootstrapToken: true}},
		{name: "mfa reset needs an email", args: []string{"--reset-mfa"}, ok: true, wantErr: true},
		{name: "unknown flag", args: []string{"--email=admin", "--force"}, ok: true, wantErr: true},
		{name: "stray argument", args: []string{"--email=admin", "now"}, ok: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok, err := parseAdminCommand(test.args, io.Discard)
			assert.Equal(t, test.ok, ok)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, command)
		})
	}
}

// The command used to start the metrics listener first and exit when the running
// server held its port, so it never reset anything; it now runs on its own.
func Test_RunAdminCommand_ResetsMFAAndPassword_WithoutStartingTheServer(t *testing.T) {
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	repository := &users_repositories.UserRepository{}
	before, err := repository.GetUserByID(admin.UserID)
	require.NoError(t, err)
	require.True(t, before.MFAEnabled)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	code := runAdminCommand(adminCommand{Email: before.Email, ResetMFA: true, NewPassword: "Reset-from-CLI-1"}, log)
	require.Equal(t, 0, code)

	after, err := repository.GetUserByID(admin.UserID)
	require.NoError(t, err)
	assert.False(t, after.MFAEnabled)
	require.NotNil(t, after.HashedPassword)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(*after.HashedPassword), []byte("Reset-from-CLI-1")))

	assert.Equal(t, 1, runAdminCommand(adminCommand{Email: "nobody@example.com", NewPassword: "x"}, log))
}
