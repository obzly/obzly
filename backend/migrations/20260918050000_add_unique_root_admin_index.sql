-- +goose Up
-- +goose StatementBegin

-- Guards CreateInitialAdmin against a real cross-replica race: on
-- simultaneous first boot (multiple pods/containers of this binary
-- starting together - see ARCHITECTURE.md's High Availability section),
-- more than one replica can observe "no root admin yet" (GetRootAdmin
-- returns nil) before either has inserted one, and both then try to
-- create one. A partial unique index makes the second INSERT fail with a
-- clean constraint-violation error instead of silently creating two
-- "root admin" rows - UserRepository.CreateInitialAdmin treats that
-- specific error as "another replica already created it", not a fatal
-- startup error.
CREATE UNIQUE INDEX idx_users_single_root_admin ON users (is_root_admin) WHERE is_root_admin = TRUE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_single_root_admin;

-- +goose StatementEnd
