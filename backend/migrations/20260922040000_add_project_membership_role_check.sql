-- +goose Up
-- +goose StatementBegin

-- Defense in depth for SECURITY_REVIEW.md Low: "ChangeMemberRole stores an
-- unvalidated role string" - the application layer now rejects a role
-- that isn't one of ProjectRole's real values
-- (membership_service.validateCanManageMembership), but this table itself
-- had no constraint of its own, relying entirely on every write path
-- remembering to validate. NOT VALID: enforced for every new/updated row
-- from here on, without requiring a scan (and failing the migration) over
-- any pre-existing row this deployment might already have - a role
-- value this constraint would reject could only have gotten there before
-- the application-layer fix above existed.
ALTER TABLE project_memberships ADD CONSTRAINT project_memberships_role_check
    CHECK (role IN ('OWNER', 'PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER')) NOT VALID;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE project_memberships DROP CONSTRAINT IF EXISTS project_memberships_role_check;

-- +goose StatementEnd
