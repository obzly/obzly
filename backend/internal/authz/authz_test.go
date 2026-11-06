package authz_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"obzly/internal/authz"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedController is a project route behind authz.Require.
type gatedController struct{}

// The legacy project checks still called directly, outside authz. Lower a
// budget when you migrate a call site to authz.Authorize; never raise one.
var legacyCheckBudget = map[string]int{
	`\.CanUserAccessProject\(`: 19,
	`\.CanUserManageProject\(`: 10,
	`GetUserProjectRole\(`:     4,
	`CanUserWriteToProject`:    0,
}

func (gatedController) RegisterRoutes(router *gin.RouterGroup) {
	router.POST("/projects/:id/gated", authz.Require(authz.ProjectWrite), func(ctx *gin.Context) {
		decision, ok := authz.FromContext(ctx)
		if !ok || decision.Role == nil {
			ctx.Status(http.StatusInternalServerError)
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"role": *decision.Role})
	})
}

func Test_Authorize_EachRoleGetsExactlyItsActions(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("Authz matrix", owner.Token, router)
	members := map[users_enums.ProjectRole]*users_dto.SignInResponseDTO{}
	for _, role := range []users_enums.ProjectRole{
		users_enums.ProjectRoleViewer, users_enums.ProjectRoleMember, users_enums.ProjectRoleAdmin,
	} {
		members[role] = users_testing.CreateTestUser(users_enums.UserRoleMember)
		projects_testing.AddMemberToProject(project, members[role], role, owner.Token, router)
	}
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)

	actions := []authz.Action{authz.ProjectRead, authz.ProjectWrite, authz.ProjectManage, authz.ProjectOwn}
	expect := []struct {
		name    string
		user    *users_dto.SignInResponseDTO
		allowed int // how many of actions, in order
	}{
		{"stranger", stranger, 0},
		{"viewer", members[users_enums.ProjectRoleViewer], 1},
		{"member", members[users_enums.ProjectRoleMember], 2},
		{"admin", members[users_enums.ProjectRoleAdmin], 3},
		{"owner", owner, 4},
		{"global admin", admin, 4},
	}
	for _, e := range expect {
		for i, action := range actions {
			decision, err := authz.Authorize(context.Background(), userOf(e.user), action, authz.Project(project.ID))
			require.NoError(t, err)
			assert.Equal(t, i < e.allowed, decision.Allowed, "%s %s", e.name, action)
		}
	}

	_, err := authz.Authorize(
		context.Background(),
		userOf(owner),
		authz.Action("project.fly"),
		authz.Project(project.ID),
	)
	assert.Error(t, err, "an unknown action is a bug, not a denial")
	decision, err := authz.Authorize(context.Background(), nil, authz.ProjectRead, authz.Project(project.ID))
	require.NoError(t, err)
	assert.False(t, decision.Allowed)
}

// A team's grant on a standalone install's projects counts like a role.
func Test_Authorize_TeamGrantCounts(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	engineer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Authz team", owner.Token, router)
	db := storage.GetDb()
	orgID, teamID := uuid.New(), uuid.New()
	require.NoError(t, db.Exec("INSERT INTO organizations (id, name, slug) VALUES (?, 'authz', ?)", orgID,
		"authz-"+orgID.String()[:8]).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM organizations WHERE id = ?", orgID) })
	require.NoError(t, db.Exec("INSERT INTO teams (id, org_id, name) VALUES (?, ?, 'team')", teamID, orgID).Error)
	require.NoError(
		t,
		db.Exec("INSERT INTO organization_memberships (org_id, user_id, role) VALUES (?, ?, 'ORG_MEMBER')",
			orgID, engineer.UserID).Error,
	)
	require.NoError(
		t,
		db.Exec("INSERT INTO team_memberships (team_id, user_id) VALUES (?, ?)", teamID, engineer.UserID).Error,
	)
	require.NoError(
		t,
		db.Exec("INSERT INTO team_grants (team_id, site_id, project_id, role) VALUES (?, '', ?, 'PROJECT_MEMBER')",
			teamID, project.ID).Error,
	)

	write, err := authz.Authorize(context.Background(), userOf(engineer), authz.ProjectWrite, authz.Project(project.ID))
	require.NoError(t, err)
	assert.True(t, write.Allowed)
	manage, err := authz.Authorize(
		context.Background(),
		userOf(engineer),
		authz.ProjectManage,
		authz.Project(project.ID),
	)
	require.NoError(t, err)
	assert.False(t, manage.Allowed)
}

// Denials are audited, once a minute per subject, action and resource -
// including on a project that doesn't exist.
func Test_Authorize_DenialsAuditedOncePerMinute(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Authz audit", owner.Token, router)
	missing := uuid.New()

	for range 3 {
		for _, id := range []uuid.UUID{project.ID, missing} {
			decision, err := authz.Authorize(
				context.Background(),
				userOf(stranger),
				authz.ProjectRead,
				authz.Project(id),
			)
			require.NoError(t, err)
			require.False(t, decision.Allowed)
		}
	}
	allowed, err := authz.Authorize(context.Background(), userOf(owner), authz.ProjectRead, authz.Project(project.ID))
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	type row struct {
		ProjectID *uuid.UUID
		Message   string
	}
	var rows []row
	require.NoError(t, storage.GetDb().Raw(
		"SELECT project_id, message FROM audit_logs WHERE user_id IN (?, ?) AND message LIKE 'Access denied%'",
		stranger.UserID, owner.UserID).Scan(&rows).Error)
	require.Len(t, rows, 2)
	byMessage := map[bool]row{}
	for _, r := range rows {
		byMessage[strings.Contains(r.Message, missing.String())] = r
	}
	require.NotNil(t, byMessage[false].ProjectID)
	assert.Equal(t, project.ID, *byMessage[false].ProjectID, "listed in the project's audit log")
	assert.Nil(t, byMessage[true].ProjectID)
	assert.Contains(t, byMessage[false].Message, "project.read")
}

// Probing random project ids audits at most 20 denials a minute per user,
// then one notice.
func Test_Authorize_DenialAuditsBoundedPerUser(t *testing.T) {
	prober := users_testing.CreateTestUser(users_enums.UserRoleMember)
	for range 30 {
		decision, err := authz.Authorize(
			context.Background(),
			userOf(prober),
			authz.ProjectRead,
			authz.Project(uuid.New()),
		)
		require.NoError(t, err)
		require.False(t, decision.Allowed)
	}

	var messages []string
	require.NoError(
		t,
		storage.GetDb().Raw("SELECT message FROM audit_logs WHERE user_id = ? AND message LIKE 'Access denied%'",
			prober.UserID).Scan(&messages).Error,
	)
	require.Len(t, messages, 21)
	suppressed := 0
	for _, message := range messages {
		if strings.Contains(message, "further denials not logged") {
			suppressed++
		}
	}
	assert.Equal(t, 1, suppressed)
}

func Test_Require_GatesTheRoute(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), gatedController{})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Authz route", owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	path := "/api/v1/projects/" + project.ID.String() + "/gated"

	test_utils.MakePostRequest(t, router, path, "Bearer "+viewer.Token, nil, http.StatusForbidden)
	var body map[string]string
	test_utils.MakePostRequestAndUnmarshal(t, router, path, "Bearer "+member.Token, nil, http.StatusOK, &body)
	assert.Equal(t, string(users_enums.ProjectRoleMember), body["role"])
	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/projects/nope/gated",
		"Bearer "+member.Token,
		nil,
		http.StatusBadRequest,
	)
	test_utils.MakePostRequest(t, router, path, "", nil, http.StatusUnauthorized)
}

// Straggler check: new access checks go through authz, so the legacy
// helpers' call sites only ever shrink.
func Test_LegacyAccessChecks_OnlyShrink(t *testing.T) {
	counts := map[string]int{}
	for _, root := range []string{"..", "../../cmd"} {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				strings.HasPrefix(filepath.ToSlash(path), "../authz/") {
				return err
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(source), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func ") {
					continue
				}
				for pattern := range legacyCheckBudget {
					if regexp.MustCompile(pattern).MatchString(line) {
						counts[pattern]++
					}
				}
			}
			return nil
		}))
	}
	for pattern, budget := range legacyCheckBudget {
		assert.LessOrEqual(t, counts[pattern], budget,
			"new access checks call authz.Authorize (or authz.Require on a route), not %s", pattern)
		assert.GreaterOrEqual(t, counts[pattern], budget,
			"%s has fewer call sites (%d): lower its budget in legacyCheckBudget", pattern, counts[pattern])
	}
}

func userOf(signIn *users_dto.SignInResponseDTO) *users_models.User {
	user, err := (&users_repositories.UserRepository{}).GetUserByID(signIn.UserID)
	if err != nil {
		panic(err)
	}
	return user
}
