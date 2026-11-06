-- +goose Up
-- +goose StatementBegin

-- SECURITY_REVIEW.md Medium: "missing FKs on project_id columns" -
-- dashboards and dashboard_folders both had a NOT NULL project_id with
-- nothing enforcing it actually points at a live project; audit_logs'
-- (nullable) project_id was the same. Without a real FK, DeleteProject's
-- own multi-step cleanup (see project_service.go's DeleteProject doc
-- comment) or any other bypass of the app layer can leave rows silently
-- pointing at a project that no longer exists. Verified directly against
-- this deployment's own data before writing this migration: audit_logs
-- already had 1076 real orphaned rows; dashboards/dashboard_folders had
-- none, but nothing was stopping it.
--
-- stream_segments (the Parquet segment catalog - internal/storage/segments)
-- deliberately does NOT get the same FK here, despite having the exact
-- same shape of gap (also verified: 33 real orphaned rows, now cleaned up
-- below anyway). That package's storage engine is intentionally decoupled
-- from the "projects" domain - project_id there is treated as an opaque
-- tenant/partition key, not a guaranteed-live foreign entity, and roughly
-- twenty of its own tests across logs/spans/metrics (compactor, uploader,
-- ingester, querier, core/tests/...) exercise the real ingest-to-Parquet
-- pipeline against fabricated project IDs specifically because that
-- package has no business knowing or caring whether a "projects" table
-- row exists - confirmed empirically: adding the FK anyway broke roughly
-- twenty of those tests package-wide. A hard REFERENCES constraint here
-- would force every one of those tests (and any future one written the
-- same way) to first insert a real project row purely to satisfy a
-- constraint the storage engine's own design says shouldn't exist at that
-- layer - the wrong tradeoff for what's actually a data-hygiene concern,
-- not a correctness one (an orphaned segment catalog row can't be queried
-- into existence by any real user: every query path requires a live
-- project + membership - projects_services.CanUserAccessProject - before
-- ever reaching the segments layer at all). Orphan prevention for this
-- table stays where it already partly is: DeleteProject's project-
-- deletion-listener cleanup, called before the project row disappears.
--
-- dashboards/dashboard_folders: a row is meaningless without its project
-- (there was never a "project-less dashboard" concept), so this is ON
-- DELETE CASCADE, matching project_memberships' own project_id FK - and
-- since neither table has orphans, both are added fully validated (no
-- NOT VALID needed).
--
-- audit_logs: the opposite call, on purpose - an audit log entry
-- ("Project deleted: X") is a historical record that's meant to survive
-- the project itself being gone, so this is ON DELETE SET NULL, the same
-- pattern its own existing user_id FK already uses. Existing orphaned
-- rows are updated to NULL (what SET NULL would have done at deletion
-- time) rather than deleted - the log message and everything else about
-- the entry stays intact.
DELETE FROM stream_segments s
WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id);

UPDATE audit_logs a
SET project_id = NULL
WHERE a.project_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = a.project_id);

ALTER TABLE dashboards
    ADD CONSTRAINT fk_dashboards_project_id FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE;

ALTER TABLE dashboard_folders
    ADD CONSTRAINT fk_dashboard_folders_project_id FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE;

ALTER TABLE audit_logs
    ADD CONSTRAINT fk_audit_logs_project_id FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE SET NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS fk_audit_logs_project_id;
ALTER TABLE dashboard_folders DROP CONSTRAINT IF EXISTS fk_dashboard_folders_project_id;
ALTER TABLE dashboards DROP CONSTRAINT IF EXISTS fk_dashboards_project_id;

-- +goose StatementEnd
