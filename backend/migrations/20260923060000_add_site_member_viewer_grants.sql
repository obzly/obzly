-- +goose Up
-- +goose StatementBegin

-- Site-wide MEMBER and VIEWER grants (control plane only - site_grants is
-- only ever written in control-plane mode): like the Site-wide ADMIN grant
-- (project_id NULL), but instead of making the user a Site admin they give
-- that role on EVERY project of the Site - PROJECT_MEMBER / PROJECT_VIEWER,
-- reconciled at each token exchange (so a project created later is picked
-- up at the user's next exchange).
-- idx_site_grants_unique already allows only one Site-wide grant per user
-- per Site, so a user is at most one of ADMIN / MEMBER / VIEWER there.
ALTER TABLE site_grants DROP CONSTRAINT site_grants_check;
ALTER TABLE site_grants ADD CONSTRAINT site_grants_check CHECK (
    (project_id IS NULL AND role IN ('ADMIN', 'MEMBER', 'VIEWER'))
    OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'))
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Rolling back drops the new Site-wide grants: the old constraint can't
-- hold them, and silently turning them into Site ADMIN would widen access.
DELETE FROM site_grants WHERE project_id IS NULL AND role IN ('MEMBER', 'VIEWER');
ALTER TABLE site_grants DROP CONSTRAINT site_grants_check;
ALTER TABLE site_grants ADD CONSTRAINT site_grants_check CHECK (
    (project_id IS NULL AND role = 'ADMIN')
    OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'))
);

-- +goose StatementEnd
