-- +goose Up
-- +goose StatementBegin
-- The organization an entry is about (SCIM provisioning), so an organization's
-- recent SCIM changes are one indexed read. No foreign key: the log outlives
-- a deleted organization.
ALTER TABLE audit_logs
    ADD COLUMN org_id UUID NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_audit_logs_org_id ON audit_logs (org_id, created_at) WHERE org_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_audit_logs_org_id;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE audit_logs DROP COLUMN org_id;
-- +goose StatementEnd
