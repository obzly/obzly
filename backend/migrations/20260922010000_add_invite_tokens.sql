-- +goose Up
-- +goose StatementBegin

-- Lets self-service SignUp for an INVITED user require proof of the invite
-- rather than just a matching email address (anyone who knew/guessed the
-- invited email could otherwise activate that account, password of their
-- choosing, inheriting whatever project role the invite carried). Set when
-- a user is invited, cleared the moment SignUp successfully consumes it
-- (single use) or the account is otherwise activated by an admin.
ALTER TABLE users ADD COLUMN invite_token_hash TEXT;
ALTER TABLE users ADD COLUMN invite_token_expires_at TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE users DROP COLUMN IF EXISTS invite_token_expires_at;
ALTER TABLE users DROP COLUMN IF EXISTS invite_token_hash;

-- +goose StatementEnd
