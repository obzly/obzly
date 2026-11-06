---
name: db-migrations
description: Writing Obzly Postgres migrations with goose. Use when adding or changing tables, columns, constraints or indexes in backend/migrations.
---

# Obzly migrations

- Files: `backend/migrations/<YYYYMMDDHHMMSS>_<snake_name>.sql`, newer than the latest existing one. Never edit a migration that has shipped; add a new one.
- goose format with both directions:
  ```sql
  -- +goose Up
  -- +goose StatementBegin
  ALTER TABLE oauth_connectors
      ADD COLUMN trust_idp_mfa BOOLEAN NOT NULL DEFAULT TRUE;
  -- +goose StatementEnd

  -- +goose Down
  -- +goose StatementBegin
  ALTER TABLE oauth_connectors DROP COLUMN trust_idp_mfa;
  -- +goose StatementEnd
  ```
- PostgreSQL only. UUID keys: `UUID PRIMARY KEY DEFAULT gen_random_uuid()`. Times: `TIMESTAMPTZ`.
- Create the table first, then each constraint and index as its own statement. Align column names and types:
  ```sql
  CREATE TABLE site_grants (
      id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      user_id    UUID NOT NULL,
      role       TEXT NOT NULL,
      created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );

  ALTER TABLE site_grants
      ADD CONSTRAINT fk_site_grants_user_id
      FOREIGN KEY (user_id)
      REFERENCES users (id) ON DELETE CASCADE;
  ```
- One migration set serves every `SERVICE_MODE`: control-plane-only tables also exist, empty, on Sites.
- New `NOT NULL` column on an existing table needs a `DEFAULT` that is safe for existing rows.
- The app runs migrations on startup (leader-locked). Apply to the local test DB with `cd backend && goose up` (see `backend-tests`), and add the matching GORM field (`gorm:"column:..."`), which `Save` writes on update.
