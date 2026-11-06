-- v0.56.0: the control plane as OIDC issuer for federated cloud
-- connections (docs/INFRASTRUCTURE.md). Microsoft Entra supports only
-- RS256 for external issuers, so this is an RSA key beside the ES256
-- signing keys, in the same JWKS. One row. Control plane only.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE cloud_federation_keys (
    id                    INTEGER     PRIMARY KEY DEFAULT 1,
    kid                   TEXT        NOT NULL,
    private_key_encrypted TEXT        NOT NULL,
    public_key_pem        TEXT        NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE cloud_federation_keys
    ADD CONSTRAINT chk_cloud_federation_keys_single_row CHECK (id = 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS cloud_federation_keys;
-- +goose StatementEnd
