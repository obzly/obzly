package organizations

import (
	"errors"
	"net/http"

	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Controller struct {
	service *Service
}

// OrgRequest creates or renames an organization.
type OrgRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// TeamRequest creates or renames a team.
type TeamRequest struct {
	Name string `json:"name"`
}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/orgs", c.ListOrgs)
	router.POST("/orgs", c.CreateOrg)
	router.GET("/orgs/:orgId", c.GetOrg)
	router.PUT("/orgs/:orgId", c.RenameOrg)
	router.DELETE("/orgs/:orgId", c.DeleteOrg)
	router.GET("/orgs/:orgId/members", c.ListMembers)
	router.POST("/orgs/:orgId/members", c.SetMember)
	router.DELETE("/orgs/:orgId/members/:userId", c.RemoveMember)
	router.PUT("/orgs/:orgId/sites/:siteId", c.AssignSite)
	router.GET("/orgs/:orgId/plan", c.GetPlan)
	router.PUT("/orgs/:orgId/plan", c.SetPlan)
	router.GET("/orgs/:orgId/teams", c.ListTeams)
	router.POST("/orgs/:orgId/teams", c.CreateTeam)
	router.PUT("/orgs/:orgId/teams/:teamId", c.RenameTeam)
	router.DELETE("/orgs/:orgId/teams/:teamId", c.DeleteTeam)
	router.POST("/orgs/:orgId/teams/:teamId/members", c.AddTeamMember)
	router.DELETE("/orgs/:orgId/teams/:teamId/members/:userId", c.RemoveTeamMember)
	router.POST("/orgs/:orgId/teams/:teamId/grants", c.AddGrant)
	router.DELETE("/orgs/:orgId/teams/:teamId/grants/:grantId", c.RemoveGrant)
	router.GET("/orgs/:orgId/scim-tokens", c.ListSCIMTokens)
	router.POST("/orgs/:orgId/scim-tokens", c.CreateSCIMToken)
	router.DELETE("/orgs/:orgId/scim-tokens/:tokenId", c.DeleteSCIMToken)
}

// ListOrgs
// @Summary List organizations
// @Description The caller's organizations (every one for a global admin)
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Success 200 {array} organizations.Organization
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs [get]
func (c *Controller) ListOrgs(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgs, err := c.service.ListOrgs(user)
	respond(ctx, orgs, err)
}

// CreateOrg
// @Summary Create an organization
// @Description Global admins; the creator becomes its owner
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body OrgRequest true "Request"
// @Success 200 {object} organizations.Organization
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs [post]
func (c *Controller) CreateOrg(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request OrgRequest
	if !bind(ctx, &request) {
		return
	}
	org, err := c.service.CreateOrg(request.Name, request.Slug, user)
	respond(ctx, org, err)
}

// GetOrg
// @Summary Get an organization
// @Description With its Sites (members)
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {object} organizations.OrgDetail
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId} [get]
func (c *Controller) GetOrg(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	org, err := c.service.GetOrg(orgID, user)
	respond(ctx, org, err)
}

// RenameOrg
// @Summary Rename an organization
// @Description Owners and admins
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param request body OrgRequest true "Request"
// @Success 200 {object} organizations.Organization
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId} [put]
func (c *Controller) RenameOrg(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	var request OrgRequest
	if !bind(ctx, &request) {
		return
	}
	org, err := c.service.RenameOrg(orgID, request.Name, user)
	respond(ctx, org, err)
}

// DeleteOrg
// @Summary Delete an organization
// @Description Global admins; not the Default organization
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId} [delete]
func (c *Controller) DeleteOrg(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Organization deleted"}, c.service.DeleteOrg(orgID, user))
}

// ListMembers
// @Summary List organization members
// @Description Members
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {array} organizations.Membership
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/members [get]
func (c *Controller) ListMembers(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	members, err := c.service.ListMembers(orgID, user)
	respond(ctx, members, err)
}

// SetMember
// @Summary Add a member or change a role
// @Description Owners and admins; only owners make owners
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param request body MemberRequest true "Request"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/members [post]
func (c *Controller) SetMember(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	var request MemberRequest
	if !bind(ctx, &request) {
		return
	}
	respond(ctx, gin.H{"message": "Member saved"}, c.service.SetMember(orgID, request, user))
}

// RemoveMember
// @Summary Remove a member
// @Description Owners and admins; also removes them from the organization's teams
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param userId path string true "userId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/members/{userId} [delete]
func (c *Controller) RemoveMember(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	userID, ok := pathID(ctx, "userId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Member removed"}, c.service.RemoveMember(orgID, userID, user))
}

// AssignSite
// @Summary Assign a Site to an organization
// @Description Global admins; the previous organization's team grants on it are removed
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param siteId path string true "siteId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/sites/{siteId} [put]
func (c *Controller) AssignSite(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Site assigned"}, c.service.AssignSite(orgID, ctx.Param("siteId"), user))
}

// ListTeams
// @Summary List teams
// @Description With members and grants (members)
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {array} organizations.Team
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams [get]
func (c *Controller) ListTeams(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teams, err := c.service.ListTeams(orgID, user)
	respond(ctx, teams, err)
}

// CreateTeam
// @Summary Create a team
// @Description Owners and admins
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param request body TeamRequest true "Request"
// @Success 200 {object} organizations.Team
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams [post]
func (c *Controller) CreateTeam(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	var request TeamRequest
	if !bind(ctx, &request) {
		return
	}
	team, err := c.service.CreateTeam(orgID, request.Name, user)
	respond(ctx, team, err)
}

// RenameTeam
// @Summary Rename a team
// @Description Owners and admins
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Param request body TeamRequest true "Request"
// @Success 200 {object} organizations.Team
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId} [put]
func (c *Controller) RenameTeam(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	var request TeamRequest
	if !bind(ctx, &request) {
		return
	}
	team, err := c.service.RenameTeam(orgID, teamID, request.Name, user)
	respond(ctx, team, err)
}

// DeleteTeam
// @Summary Delete a team
// @Description Owners and admins; its members lose its grants
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId} [delete]
func (c *Controller) DeleteTeam(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Team deleted"}, c.service.DeleteTeam(orgID, teamID, user))
}

// AddTeamMember
// @Summary Add a team member
// @Description Owners and admins; adds the user to the organization if needed
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Param request body MemberRequest true "Request"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId}/members [post]
func (c *Controller) AddTeamMember(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	var request MemberRequest
	if !bind(ctx, &request) {
		return
	}
	respond(ctx, gin.H{"message": "Team member added"}, c.service.AddTeamMember(orgID, teamID, request, user))
}

// RemoveTeamMember
// @Summary Remove a team member
// @Description Owners and admins
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Param userId path string true "userId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId}/members/{userId} [delete]
func (c *Controller) RemoveTeamMember(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	userID, ok := pathID(ctx, "userId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Team member removed"}, c.service.RemoveTeamMember(orgID, teamID, userID, user))
}

// AddGrant
// @Summary Give a team a role
// @Description On one of the organization's Sites (or a project of it); site "" is a standalone install's own projects. Owners and admins
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Param request body GrantRequest true "Request"
// @Success 200 {object} organizations.Grant
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId}/grants [post]
func (c *Controller) AddGrant(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	var request GrantRequest
	if !bind(ctx, &request) {
		return
	}
	grant, err := c.service.AddGrant(orgID, teamID, request, user)
	respond(ctx, grant, err)
}

// RemoveGrant
// @Summary Remove a team's role
// @Description Owners and admins
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param teamId path string true "teamId"
// @Param grantId path string true "grantId"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/teams/{teamId}/grants/{grantId} [delete]
func (c *Controller) RemoveGrant(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	teamID, ok := pathID(ctx, "teamId")
	if !ok {
		return
	}
	grantID, ok := pathID(ctx, "grantId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "Team grant removed"}, c.service.RemoveGrant(orgID, teamID, grantID, user))
}

// ListSCIMTokens
// @Summary List SCIM tokens
// @Description Owners and global admins
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Success 200 {array} organizations.SCIMToken
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/scim-tokens [get]
func (c *Controller) ListSCIMTokens(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	tokens, err := c.service.ListSCIMTokens(orgID, user)
	respond(ctx, tokens, err)
}

// CreateSCIMToken
// @Summary Create a SCIM token
// @Description Owners and global admins; the token is returned only once
// @Tags organizations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param request body SCIMTokenRequest true "Request"
// @Success 200 {object} organizations.NewSCIMToken
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /orgs/{orgId}/scim-tokens [post]
func (c *Controller) CreateSCIMToken(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	var request SCIMTokenRequest
	if !bind(ctx, &request) {
		return
	}
	token, err := c.service.CreateSCIMToken(orgID, request.Name, user)
	respond(ctx, token, err)
}

// DeleteSCIMToken
// @Summary Revoke a SCIM token
// @Description Owners and global admins
// @Tags organizations
// @Produce json
// @Security BearerAuth
// @Param orgId path string true "orgId"
// @Param tokenId path string true "tokenId"
// @Success 200 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /orgs/{orgId}/scim-tokens/{tokenId} [delete]
func (c *Controller) DeleteSCIMToken(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	orgID, ok := pathID(ctx, "orgId")
	if !ok {
		return
	}
	tokenID, ok := pathID(ctx, "tokenId")
	if !ok {
		return
	}
	respond(ctx, gin.H{"message": "SCIM token revoked"}, c.service.DeleteSCIMToken(orgID, tokenID, user))
}

func pathID(ctx *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param(name))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + name})
		return uuid.Nil, false
	}
	return id, true
}

func bind(ctx *gin.Context, target any) bool {
	if err := ctx.ShouldBindJSON(target); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return false
	}
	return true
}

func respond(ctx *gin.Context, body any, err error) {
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, body)
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrPlanLimit):
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
