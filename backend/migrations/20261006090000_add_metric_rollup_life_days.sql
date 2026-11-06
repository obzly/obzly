-- +goose Up
-- +goose StatementBegin
ALTER TABLE projects
    ADD COLUMN metric_rollup_life_days INT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects DROP COLUMN metric_rollup_life_days;
-- +goose StatementEnd
