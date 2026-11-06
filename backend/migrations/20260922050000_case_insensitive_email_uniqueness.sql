-- +goose Up
-- +goose StatementBegin

-- SECURITY_REVIEW.md Medium: "Case-sensitive email uniqueness lets
-- 'user@x.com' and 'User@x.com' register as two distinct accounts". Every
-- application-layer email write/lookup now normalizes to lowercase
-- (users_repositories.normalizeEmail - CreateUser, CreateInitialAdmin,
-- GetUserByEmail, UpdateUserInfo, SyncRootAdminEmail all go through it as
-- of this same change), so no *new* case-variant duplicate can be
-- created from here on. This migration brings the database itself in
-- line - both any pre-existing data, and the constraint that enforces it
-- going forward (defense in depth: a future write path that forgets to
-- normalize still can't sneak a duplicate past the database).
--
-- Step 1: for any pre-existing case-variant duplicates (e.g. one row for
-- "user@x.com" and another for "User@x.com" - none on this deployment as
-- of writing, checked directly, but this must be safe for any
-- deployment), keep the oldest account's address as the canonical one
-- (it's lowercased in step 2 like every other row) and tag every other
-- row sharing that address, case-insensitively, with a "+dupe-<short-id>"
-- suffix before the @. This does NOT merge or delete any account - two
-- real distinct people who happen to share a case-variant address is not
-- something a migration should ever guess how to resolve; tagging just
-- frees up the canonical address so step 3's constraint can be added, and
-- leaves an unmistakable trail for an operator to follow up with the
-- affected accounts directly.
DO $$
DECLARE
    dup RECORD;
BEGIN
    FOR dup IN
        SELECT id, email,
               row_number() OVER (
                   PARTITION BY LOWER(email) ORDER BY created_at ASC, id ASC
               ) AS rn
        FROM users
    LOOP
        IF dup.rn > 1 THEN
            UPDATE users
            SET email = regexp_replace(dup.email, '@', '+dupe-' || substr(dup.id::text, 1, 8) || '@')
            WHERE id = dup.id;
        END IF;
    END LOOP;
END $$;

-- Step 2: normalize every remaining email to lowercase, matching what
-- every application write path now does going forward.
UPDATE users SET email = LOWER(email) WHERE email <> LOWER(email);

-- Step 3: replace the old case-SENSITIVE unique constraint with a
-- case-INsensitive unique index - this is the actual, database-level
-- enforcement of what steps 1-2 just brought existing data in line with.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower ON users (LOWER(email));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_email_lower;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

-- +goose StatementEnd
