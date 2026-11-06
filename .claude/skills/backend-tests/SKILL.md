---
name: backend-tests
description: How to write and run Obzly Go tests (real Postgres/Valkey, HTTP-level controller tests, naming). Use when adding or fixing backend tests or verifying a backend change.
---

# Obzly backend tests

## Style
- Name: `Test_WhatWeDo_WhatWeExpect` or `Test_WhatWeDo_WhichConditions_WhatWeExpect`
  (e.g. `Test_DeleteApiKey_WhenUserIsProjectMember_ReturnsForbidden`).
- Prefer testing through HTTP: build a gin router with the real controllers and call it with `test_utils.MakeGetRequest` / `MakePostRequestAndUnmarshal` (`internal/util/testing`).
- Unit tests are fine for pure logic with no endpoint (parsers, pattern hashing, DTO conversion).
- Test files are `controller_test.go` / `service_test.go` / `<topic>_test.go`, never `integration_test.go`.
- Reuse helpers: `users_testing.CreateTestUser(role)` (admins get MFA enabled), `projects_testing.*`, fake OIDC server in `users/controllers/sso_testhelpers_test.go`. Put new shared helpers in the feature's `testing/` package.
- Always add a regression test with a bug fix.

## Environment
Tests hit real services configured by `backend/.env` (Postgres on 5437, Valkey, MinIO). Packages that touch log/metric storage need a `TestMain` calling `SetupRepository()` from `logs/testsupport` or `spans/testsupport` (see any `testmain_test.go`).

New migration? Apply it to the test DB first. goose reads `GOOSE_DRIVER/GOOSE_DBSTRING/GOOSE_MIGRATION_DIR` from `backend/.env` by itself - don't pass driver/DSN positionally (it then treats "postgres" as the command):
```bash
cd backend && goose up && goose status
```

## Running
```bash
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
cd backend
go test ./internal/features/<feature>/...            # the package you changed
go test ./internal/features/users/controllers/ -run 'Test_SSOCallback' -v
go test -failfast ./internal/...                      # what CI runs
```
Run the tests after writing them and report real results. The pre-push hook does not run tests.
