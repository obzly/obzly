-- +goose Up
-- +goose StatementBegin

-- Per-connector "the identity provider enforces MFA" switch. When true, a
-- sign-in through this connector is not asked for Obzly's own TOTP code or
-- enrollment - not even for an Admin, whose MFA is otherwise mandatory -
-- because the provider already required a second factor before it issued
-- the ID token. The session is marked (auth_origin = "sso-idp-mfa") so the
-- MFA gate lets it through; a password sign-in to the same account is still
-- held to local MFA. Defaults to true: an organization that puts its admins
-- behind an SSO connector almost always enforces MFA at that provider, and a
-- second, separate authenticator for the same login is what users hit as a
-- dead end. Admins whose provider does not enforce MFA turn it off.
ALTER TABLE oauth_connectors
    ADD COLUMN trust_idp_mfa BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE oauth_connectors DROP COLUMN trust_idp_mfa;

-- +goose StatementEnd
