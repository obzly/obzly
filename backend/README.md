# Obzly backend

Go service (Gin, GORM, DuckDB over Parquet segments, Postgres catalog, NATS JetStream). One binary
runs every role, chosen by `SERVICE_MODE`: `all` (default), `api`, `worker`, `broker`, `querier`,
`control-plane` - see the main README's "Enterprise-grade logging and monitoring" section and
`../ARCHITECTURE.md`.

## Before running

```bash
cp .env.development.example .env
cp docker-compose.yml.example docker-compose.yml   # development only
docker compose up -d
```

## Run

```bash
go build -o main ./cmd   # build
make run                 # run
make swagger             # regenerate swagger (CI fails if backend/swagger is stale)
```

Swagger UI: http://localhost:4005/api/v1/docs/swagger/index.html#/

## Test

Tests use the real Postgres and Valkey from `.env` (object storage is in memory, `mem://`). Apply migrations to the test database
first (`goose up`; goose reads `GOOSE_*` from `.env`).

```bash
make test                                   # or: go test ./internal/... ./cmd/...
go test -timeout 40m ./internal/...         # the logs cleanup suite alone takes 15-35 min
make lint                                   # golangci-lint, same as the pre-push hook
```

## Migrations

```bash
make migration-create name=MIGRATION_NAME
make migration-up
make migration-down   # roll back the latest
```

## Layout

- `cmd/main.go` - startup for every `SERVICE_MODE`; `cmd/reconcile.go` - `main reconcile`.
- `internal/features/<feature>/` - features (logs, spans, metrics, dashboards, alerting, users,
  projects, organizations (with SCIM), control_plane, ...): small ones flat (`controller.go`, `service.go`, `repository.go`,
  `dto.go`, `di.go`), larger ones in `controllers/`, `services/`, `models/`, ... sub-packages.
- `internal/storage/segments/` - Parquet segments: writer, catalog, compaction levels, object
  storage, the query-time tail, `Reconcile` and the integrity `Audit`.
- `internal/authz/` - the one authorization path: `Authorize` and the `Require` route gate.
- `internal/query/` - distributed query: coordinator, in-process and remote executors (NATS
  dispatch, gRPC results), scan budget.
- `internal/broker/`, `internal/ingest/` - the JetStream ingest buffer and its shard runners.
- `internal/telemetry/` - Obzly's own Prometheus metrics and self-tracing.
- `cmd/probe/` - the `obzly-probe` binary (Synthetics checks, probe groups, cloud scans);
  `internal/synthetics/` - its checks, wire types and OTLP reporting; `internal/cloud/discovery/` -
  AWS and Azure inventory discovery for cloud connections.
- `migrations/` - goose SQL migrations, run automatically at startup.

Project conventions live in `.claude/skills/` at the repo root (backend-go, backend-tests,
db-migrations, frontend-react, site-scoped-api).
