---
name: backend-go
description: Obzly Go backend conventions (Gin, GORM, DI, Swagger). Use when writing or changing Go code under backend/ - features, controllers, services, repositories, DI wiring.
---

# Obzly backend (Go)

## Layout
- Features live in `backend/internal/features/<feature>/`.
- Small feature: flat files `controller.go`, `service.go`, `repository.go`, `models.go`, `dto.go`, `di.go` (reference: `audit_logs/`).
- Larger feature: sub-packages `controllers/ services/ repositories/ models/ dto/`, each with its own `di.go` (reference: `control_plane/`, `users/sso/`).
- Package names are `<feature>_<layer>`, e.g. `control_plane_services`.

## File order
Types and constants, then exported functions/methods, then unexported helpers at the bottom. Applies to every Go file, tests included.

## DI (`di.go`)
- Package-level singletons plus `GetXxx()` getters.
- Build services/controllers with **positional** struct literals, so adding a dependency fails to compile until every literal is updated:
  ```go
  var orderController = &OrderController{orderService, users_services.GetUserService()}
  ```
- Add new fields at the end of the struct.
- Optional or cyclic dependencies use a setter (`SetAuditLogWriter`) called from `main.go` or `SetupDependencies()`.

## Controllers
- One controller per feature with `RegisterRoutes(*gin.RouterGroup)`; handlers are named for what they do (`GetGlobalAuditLogs`), take `*gin.Context`.
- Current user: `users_middleware.GetUserFromContext(ctx)`.
- Errors: `ctx.JSON(status, gin.H{"error": msg})`; map known service errors (sentinel `errors.Is`) to 400/403/404, anything else to 500 with a generic message.
- Every route gets Swagger annotations (`@Summary @Tags @Produce @Security BearerAuth @Param @Success @Failure @Router`). CI runs `swag init`, so a broken annotation fails the build.

## Rules
- Always `time.Now().UTC()`, never bare `time.Now()`.
- Permission checks live in services (`user.CanManageUsers()`, project role checks), not controllers.
- Security-sensitive actions write an audit log via the injected `AuditLogWriter`.
- New HTTP routes: decide Site vs control plane first - see the `site-scoped-api` skill.

## Signal names and levels
- Stored attribute, field, metric and label names are PromQL-safe: `util/attrnames.Normalize` drops a leading `resource.` and turns anything outside `[a-zA-Z0-9_]` into `_` (`resource.k8s.pod.name` -> `k8s_pod_name`). Metric names use `NormalizeMetricName`; whole maps use `NormalizeMap` (record > scope > resource on collision). Frontend mirror: `shared/lib/attributeNames.ts`.
- Normalize at every ingest path and every user-supplied name (search fields, SQL, label names, matchers). PromQL goes through `metrics_promql.NormalizePromQL` / `NormalizeMatchers`, so dotted names still work.
- Log levels are stored lowercase and matched case-insensitively (`logs_core.LowercaseCondition`, `lower(level)` in SQL).
- Never hardcode dotted names (`k8s.pod.name`, `pattern.hash`) in queries, templates or tool docs.

## Segments and schema versions
- `stream_segments.schema_version`: 1 = legacy (dotted names, uppercase levels), 2 = current (`segments.CurrentSchemaVersion`).
- Queriers build their FROM clause with `segments.SourceExpr(stream, files)` - it rewrites legacy files on read via DuckDB UDFs (register with `segments.RegisterNormalizationFunctions` on any new DuckDB handle). Don't read Parquet paths directly.
- A storage format change means: bump the version, add read-time `LegacyReplacements`, and let the `datamigration` worker rewrite old segments. Compaction never merges different versions.

## Comments
Short, and only for the *why* (a business rule, a non-obvious constraint, a past bug). No comments that restate the code or the function name.

## Before finishing
From `backend/` (Go lives in `/usr/local/go/bin`, tools in `~/go/bin`):
```bash
gofmt -w . && golangci-lint run ./...   # same checks as the pre-push hook
go build ./... && go vet ./...
```
Then run the relevant tests (`backend-tests` skill).
