package control_plane_services

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	users_enums "obzly/internal/features/users/enums"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Site refusal is negative-cached for 30s; granting access must clear it (and any
// cached token) so the grant works on the very next request.
func Test_CreateGrant_InvalidatesCachedSiteRefusal(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	t.Cleanup(SetEgressPolicyForTests(func(net.IP) error { return nil }))

	var callCount int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no access"}`))
	}))
	defer site.Close()

	user := createTestUserRow(t)
	siteRow := createTestSiteRow(t, site.URL)
	// A first grant so an assertion can be minted; the Site still refuses.
	projectID := uuid.New()
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteRow.ID, ProjectID: &projectID, Role: "PROJECT_VIEWER", CreatedBy: user.ID,
	}))
	_, err := siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.Error(t, err)
	_, _ = siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	require.EqualValues(t, 1, atomic.LoadInt64(&callCount), "precondition: the refusal is cached")

	admin := createTestUserRow(t)
	admin.Role = users_enums.UserRoleAdmin
	admin.IsRootAdmin = true // Site-wide ADMIN grants are root-only
	require.NoError(t, (&users_repositories.UserRepository{}).UpdateUserRole(admin.ID, users_enums.UserRoleAdmin))

	_, err = grantService.CreateGrant(admin, control_plane_dto.CreateGrantRequestDTO{
		UserID: user.ID, SiteID: siteRow.ID, Role: "ADMIN",
	})
	require.NoError(t, err)

	_, _ = siteTokenService.TokenFor(user, siteRow, []string{"pwd"})
	assert.EqualValues(t, 2, atomic.LoadInt64(&callCount), "a new grant must clear the cached refusal")
}

// Only the root admin may grant Site-wide ADMIN, directly or via an SSO group mapping.
// Other grants stay open to any central admin.
func Test_CreateGrant_SiteWideAdmin_WhenNotRootAdmin_IsForbidden(t *testing.T) {
	admin := createTestUserRow(t)
	admin.Role = users_enums.UserRoleAdmin
	siteRow := createTestSiteRow(t, "https://site.example.com")
	user := createTestUserRow(t)

	_, err := grantService.CreateGrant(admin, control_plane_dto.CreateGrantRequestDTO{
		UserID: user.ID, SiteID: siteRow.ID, Role: GrantRoleSiteAdmin,
	})
	assert.ErrorIs(t, err, ErrGrantSiteAdminRootOnly)

	_, err = grantService.CreateGrant(admin, control_plane_dto.CreateGrantRequestDTO{
		UserID: user.ID, SiteID: siteRow.ID, Role: GrantRoleSiteMember,
	})
	assert.NoError(t, err)

	_, err = (&SSOSiteMappingService{}).Create(admin, uuid.New(), "admins", siteRow.ID, nil, GrantRoleSiteAdmin)
	assert.ErrorIs(t, err, ErrGrantSiteAdminRootOnly)
}

// Site-wide MEMBER / VIEWER grants: valid only without a project, carried
// to the Site as all_projects_role, with site_role left at MEMBER.
func Test_SiteWideMemberViewerGrants_ValidateAndMint(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())

	admin := createTestUserRow(t)
	admin.Role = users_enums.UserRoleAdmin
	siteRow := createTestSiteRow(t, "https://site.example.com")
	projectID := uuid.New()

	cases := []struct {
		role        string
		projectID   *uuid.UUID
		wantErr     bool
		wantAllRole string
	}{
		{"MEMBER", nil, false, "PROJECT_MEMBER"},
		{"VIEWER", nil, false, "PROJECT_VIEWER"},
		{"MEMBER", &projectID, true, ""},        // Site-wide roles take no project
		{"PROJECT_VIEWER", nil, true, ""},       // project roles need one
		{"PROJECT_OWNER", &projectID, true, ""}, // never grantable centrally
	}
	for _, tc := range cases {
		user := createTestUserRow(t)
		_, err := grantService.CreateGrant(admin, control_plane_dto.CreateGrantRequestDTO{
			UserID: user.ID, SiteID: siteRow.ID, ProjectID: tc.projectID, Role: tc.role,
		})
		if tc.wantErr {
			assert.ErrorIs(t, err, ErrGrantInvalidRole, tc.role)
			continue
		}
		require.NoError(t, err, tc.role)

		grant, err := grantService.ResolveEffectiveGrant(user.ID, siteRow.ID)
		require.NoError(t, err)
		assert.True(t, grant.HasAnyAccess())
		assert.False(t, grant.IsAdmin())

		raw, err := assertionService.Mint(user, siteRow.ID, "https://cp.example.com", []string{"pwd"})
		require.NoError(t, err)
		claims := decodeUnverifiedJWTPayload(t, raw)
		assert.Equal(t, "MEMBER", claims["site_role"], "site_role keeps its old meaning")
		assert.Equal(t, tc.wantAllRole, claims["all_projects_role"])
	}
}

// A Site ADMIN grant carries no all_projects_role (ADMIN already sees every
// project, via site_role).
func Test_SiteAdminGrant_HasNoAllProjectsRole(t *testing.T) {
	require.NoError(t, signingKeyService.EnsureActiveKey())
	user := createTestUserRow(t)
	siteRow := createTestSiteRow(t, "https://site.example.com")
	require.NoError(t, grantRepository.Create(&control_plane_models.SiteGrant{
		UserID: user.ID, SiteID: siteRow.ID, Role: "ADMIN", CreatedBy: user.ID,
	}))

	raw, err := assertionService.Mint(user, siteRow.ID, "https://cp.example.com", []string{"pwd"})
	require.NoError(t, err)
	claims := decodeUnverifiedJWTPayload(t, raw)
	assert.Equal(t, "ADMIN", claims["site_role"])
	_, present := claims["all_projects_role"]
	assert.False(t, present)
}
