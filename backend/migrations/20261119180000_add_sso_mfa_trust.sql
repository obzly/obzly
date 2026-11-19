-- +goose Up
-- +goose StatementBegin

-- Three-way replacement for trust_idp_mfa: how far a sign-in through this
-- connector may skip Obzly's own TOTP.
--   off    - never; Obzly's MFA applies wherever policy requires it.
--   amr    - only when the ID token's amr shows a second factor (what
--            trust_idp_mfa = true meant).
--   always - every sign-in through this connector: the admin states that the
--            provider requires MFA for everyone who can sign in through it,
--            for providers that never report it in amr (e.g. Cognito
--            federating Google sends amr ["fed"]).
-- A password sign-in to the same account is still held to local MFA.
ALTER TABLE oauth_connectors
    ADD COLUMN mfa_trust TEXT NOT NULL DEFAULT 'off';

-- +goose StatementEnd

-- +goose StatementBegin

ALTER TABLE oauth_connectors
    ADD CONSTRAINT chk_oauth_connectors_mfa_trust
    CHECK (mfa_trust IN ('off', 'amr', 'always'));

-- +goose StatementEnd

-- +goose StatementBegin

UPDATE oauth_connectors SET mfa_trust = 'amr' WHERE trust_idp_mfa = TRUE;

-- +goose StatementEnd

-- +goose StatementBegin

-- trust_idp_mfa stays (an older pod mid-rollout, or after a rollback, still
-- reads it); the app keeps it in step as mfa_trust <> 'off', which such a
-- pod reads as "amr" - never more trusting than the new setting. Its old
-- default (TRUE) would make a row inserted without it trusted, so it now
-- defaults to FALSE.
ALTER TABLE oauth_connectors
    ALTER COLUMN trust_idp_mfa SET DEFAULT FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

UPDATE oauth_connectors SET trust_idp_mfa = (mfa_trust <> 'off');

-- +goose StatementEnd

-- +goose StatementBegin

ALTER TABLE oauth_connectors
    ALTER COLUMN trust_idp_mfa SET DEFAULT TRUE;

-- +goose StatementEnd

-- +goose StatementBegin

ALTER TABLE oauth_connectors DROP CONSTRAINT chk_oauth_connectors_mfa_trust;

-- +goose StatementEnd

-- +goose StatementBegin

ALTER TABLE oauth_connectors DROP COLUMN mfa_trust;

-- +goose StatementEnd
