-- +goose Up
-- +goose StatementBegin

-- MFA (TOTP). Enforcement policy (Admin required, Member optional) is
-- computed from the user's role at auth time, not stored - see
-- users_services.completeAuthentication. mfa_secret_encrypted holds an
-- AES-256-GCM ciphertext (SecretKeyRepository.Encrypt), same convention as
-- oauth_connectors.client_secret_encrypted; it's written as soon as a user
-- starts setup, before mfa_enabled flips true, so a half-finished setup
-- doesn't leave stale plaintext state. mfa_last_used_step is the anti-
-- replay guard: the last TOTP time-step (unix_time/30) that was
-- successfully consumed, so the exact same 30s code can never be accepted
-- twice, and no earlier code can be replayed after a later one succeeds.
ALTER TABLE users ADD COLUMN mfa_secret_encrypted TEXT;
ALTER TABLE users ADD COLUMN mfa_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN mfa_last_used_step BIGINT NOT NULL DEFAULT 0;

-- is_root_admin replaces the old "email == 'admin'" string-equality check
-- (still used in a handful of places as a stand-in for "the bootstrap
-- super-admin account") with an explicit flag, so that account's email can
-- be changed via ADMIN_EMAIL without breaking those checks - see
-- UserRepository.SyncRootAdminEmail.
ALTER TABLE users ADD COLUMN is_root_admin BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE users SET is_root_admin = TRUE WHERE email = 'admin';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE users DROP COLUMN mfa_secret_encrypted;
ALTER TABLE users DROP COLUMN mfa_enabled;
ALTER TABLE users DROP COLUMN mfa_last_used_step;
ALTER TABLE users DROP COLUMN is_root_admin;

-- +goose StatementEnd
