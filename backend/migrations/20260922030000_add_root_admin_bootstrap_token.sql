-- +goose Up
-- +goose StatementBegin

-- Closes the "first-come-first-served" bootstrap: POST
-- /users/admin/set-password used to be unauthenticated and unlimited (only
-- guarded by "does the account already have a password"), so whoever hit
-- it first after the root admin account was created - not necessarily the
-- real operator - claimed the account. The random token generated at
-- CreateInitialAdmin time and printed to the server's own logs is
-- something only someone with access to those logs (the real operator)
-- can read.
ALTER TABLE users ADD COLUMN bootstrap_token_hash TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE users DROP COLUMN IF EXISTS bootstrap_token_hash;

-- +goose StatementEnd
