-- +goose Up
-- +goose StatementBegin

ALTER TABLE users 
    ADD COLUMN github_oauth_id TEXT,
    ADD COLUMN google_oauth_id TEXT;

CREATE INDEX idx_users_github_oauth_id ON users (github_oauth_id) WHERE github_oauth_id IS NOT NULL;
CREATE INDEX idx_users_google_oauth_id ON users (google_oauth_id) WHERE google_oauth_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_google_oauth_id;
DROP INDEX IF EXISTS idx_users_github_oauth_id;

ALTER TABLE users 
    DROP COLUMN google_oauth_id,
    DROP COLUMN github_oauth_id;

-- +goose StatementEnd
