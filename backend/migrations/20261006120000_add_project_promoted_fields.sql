-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_promoted_fields (
    project_id UUID        NOT NULL,
    slot       TEXT        NOT NULL,
    field      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retired_at TIMESTAMPTZ,
    PRIMARY KEY (project_id, slot)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE project_promoted_fields
    ADD CONSTRAINT fk_project_promoted_fields_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_project_promoted_fields_active
    ON project_promoted_fields (project_id, field) WHERE retired_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE project_promoted_fields;
-- +goose StatementEnd
