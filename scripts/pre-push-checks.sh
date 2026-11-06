#!/usr/bin/env bash
# Runs the same fast checks ci-release.yml's lint-backend/lint-frontend
# jobs run, locally, before a push - so formatting/lint drift (the kind
# that's caught golangci-lint/prettier issues twice in CI already) shows
# up in seconds instead of after a multi-minute CI round trip.
#
# Deliberately does NOT run test-backend's DB-backed test suite (that
# needs Postgres/Valkey up via docker-compose.yml.example and takes much
# longer) - this is meant to be fast enough to run on every push without
# it being annoying. Run the real test suite yourself before anything
# that actually touches query/storage logic.
#
# Installed as this repo's git pre-push hook (see .git/hooks/pre-push,
# not committed - git hooks live outside version control by default).
# To skip once in a real emergency: git push --no-verify.
#
# To install the hook on a fresh clone:
#   printf '#!/usr/bin/env bash\nexec "$(git rev-parse --show-toplevel)/scripts/pre-push-checks.sh"\n' > .git/hooks/pre-push
#   chmod +x .git/hooks/pre-push
#
# Requires locally: Node 26.8.2 + npm (frontend/package.json's engines,
# same version pinned in Dockerfile/ci-release.yml), Go 1.27.1, a C
# compiler (this project's DuckDB bindings are real cgo - CGO_ENABLED=1
# and gcc/build-essential must both be present or `go build`/golangci-lint
# fail with confusing "undefined: bindings.*" errors that have nothing to
# do with your actual code), golangci-lint v2.13.2
# (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2).

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

fail=0

echo "==> backend: gofmt"
if [ -n "$(gofmt -l backend)" ]; then
  echo "gofmt found unformatted files:"
  gofmt -l backend
  echo "Run: gofmt -w backend"
  fail=1
fi

echo "==> backend: golangci-lint"
if ! (cd backend && golangci-lint run); then
  fail=1
fi

echo "==> backend: go mod tidy"
if ! (cd backend && go mod tidy && git diff --exit-code go.mod go.sum); then
  echo "go mod tidy made changes - commit backend/go.mod and backend/go.sum"
  fail=1
fi

echo "==> frontend: prettier"
if ! (cd frontend && npm run format --silent && git diff --exit-code); then
  echo "Prettier made changes - run 'npm run format' in frontend/ and commit the changes"
  fail=1
fi

echo "==> frontend: eslint"
if ! (cd frontend && npm run lint --silent); then
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "Pre-push checks failed - fix the above before pushing (or 'git push --no-verify' to skip, not recommended)."
  exit 1
fi

echo ""
echo "All pre-push checks passed."
