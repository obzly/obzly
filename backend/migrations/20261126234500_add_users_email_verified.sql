-- +goose Up
-- +goose StatementBegin
-- email_verified: someone other than the account vouched for its email (an
-- admin's invite, SCIM, or an SSO/OAuth sign-in with a trusted email). A
-- control plane sends it in the sign-in assertion; a Site matches a local
-- account by email only when it is true.
ALTER TABLE users
    ADD COLUMN email_verified BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose StatementBegin
-- Existing accounts: true only where the email provably wasn't typed by the
-- account itself. No password means it came from an invite, SCIM, SSO or a
-- control plane (an email can't be self-changed without a password); the root
-- admin's comes from ADMIN_EMAIL; a password account counts when the audit
-- log shows it completed an invite for the email it still has. Self-registered
-- accounts and ones that changed their email stay false.
UPDATE users u
SET email_verified = TRUE
WHERE u.is_root_admin
   OR COALESCE(u.hashed_password, '') = ''
   OR EXISTS (
       SELECT 1
       FROM audit_logs a
       WHERE a.user_id = u.id
         AND a.message = 'Invited user completed registration: ' || u.email
   );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN email_verified;
-- +goose StatementEnd
