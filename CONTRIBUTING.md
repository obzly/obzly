# Contributing to Obzly

Thanks for helping. This page covers how to get a working setup, what a change needs before it can
be merged, and how releases follow from commit messages.

## Where to start

- **Bugs and ideas**: open an issue with the templates provided. Security problems go through
  [SECURITY.md](SECURITY.md), never a public issue.
- **The design**: [ARCHITECTURE.md](ARCHITECTURE.md) is the source of truth; [ROADMAP.md](ROADMAP.md)
  says what's planned. For anything bigger than a bug fix, open an issue first so the approach can be
  agreed before you write it.

## Development setup

You need Go (see `backend/go.mod`), Node (see `.github/workflows/ci-release.yml`), and Docker for
Postgres and Valkey.

```bash
cd backend
cp .env.development.example .env
cp docker-compose.yml.example docker-compose.yml
docker compose up -d                     # Postgres and Valkey, for development and tests
make run                                 # the server on :4005 (migrations run at startup)

cd ../frontend
npm install
npm run dev                              # the UI on :5173, calling :4005
```

[`backend/README.md`](backend/README.md) and [`frontend/README.md`](frontend/README.md) describe the
layout and conventions; [`docs/configuration.md`](docs/configuration.md) lists every setting.

## Before you open a pull request

- **Tests**: backend changes come with tests, preferably through the HTTP API with the real
  controllers (see existing `*_test.go`). Bug fixes come with a test that fails without the fix.
  Run the packages you touched; `go test -timeout 40m ./internal/... ./cmd/...` runs everything
  (it needs the services from `docker compose up`).
- **Checks**: `scripts/pre-push-checks.sh` runs what CI's lint jobs run - gofmt/golangci-lint,
  Swagger freshness (`make swagger` regenerates `backend/swagger`, which is committed), `go mod tidy`,
  Prettier and ESLint. Install it as your pre-push hook:
  ```bash
  printf '#!/usr/bin/env bash\nexec "$(git rev-parse --show-toplevel)/scripts/pre-push-checks.sh"\n' > .git/hooks/pre-push
  chmod +x .git/hooks/pre-push
  ```
- **Migrations**: a new goose file under `backend/migrations/`, never an edit to one that shipped;
  additive (expand now, contract a release later), so the previous release keeps working against it.
- **Docs**: a new setting goes in `docs/configuration.md` (and, commented out, in
  `backend/.env.production.example`); a user-facing feature in the README; a change operators must act
  on in `docs/runbooks/upgrade.md`.

## Commit messages and versioning

Every merge to `main` that contains a releasable commit cuts a release (`.github/workflows/ci-release.yml`).
The version follows [Semantic Versioning](https://semver.org) and is derived from commit subjects:

| Subject starts with | Release |
| --- | --- |
| `FEATURE (scope): ...` | minor |
| `FIX (scope): ...`, `REFACTOR (scope): ...` | patch |
| `DOCS`, `TEST`, `CHORE`, ... | none on its own |
| `TYPE (scope)!: ...`, or `BREAKING CHANGE` in the subject | major |

Add `[skip-release]` to a merge commit's message to build without releasing. Keep subjects short and
in the imperative ("add", "fix"); the body says why. Each release's notes are generated from these
subjects - see [CHANGELOG.md](CHANGELOG.md).

Until 1.0, minor releases may include changes operators must act on; they're always listed in the
upgrade runbook. Rolling back one release is supported; two is not.

## Code of conduct

Participation is governed by our [Code of Conduct](CODE_OF_CONDUCT.md).

## License

By contributing you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
