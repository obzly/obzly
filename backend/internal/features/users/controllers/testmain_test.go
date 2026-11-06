package users_controllers

import (
	"fmt"
	"os"
	"testing"

	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"
)

// TestMain disables SSO connectors left by earlier local runs: the public list
// runs discovery against each, and stale ones made tests flaky. It also holds
// test_utils.RootAdminLock for the whole run, since these tests act as the root admin.
func TestMain(m *testing.M) {
	release, err := test_utils.AcquireExclusiveGlobal(test_utils.RootAdminLock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to acquire the root admin lock:", err)
		os.Exit(1)
	}

	storage.GetDb().Exec("UPDATE oauth_connectors SET enabled = false WHERE enabled")
	code := m.Run()
	release()
	os.Exit(code)
}
