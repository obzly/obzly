package organizations

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"obzly/internal/util/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimContentType = "application/scim+json; charset=utf-8"
	scimTokenKey    = "scimToken"
	scimBodyLimit   = 1 << 20
)

type SCIMName struct {
	Formatted  string `json:"formatted,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

type SCIMEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type SCIMMeta struct {
	ResourceType string     `json:"resourceType"`
	Created      *time.Time `json:"created,omitempty"`
	Location     string     `json:"location"`
}

// SCIMUser is a SCIM User resource.
type SCIMUser struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id"`
	ExternalID  *string     `json:"externalId,omitempty"`
	UserName    string      `json:"userName"`
	Name        SCIMName    `json:"name"`
	DisplayName string      `json:"displayName"`
	Emails      []SCIMEmail `json:"emails"`
	Active      bool        `json:"active"`
	Meta        SCIMMeta    `json:"meta"`
}

type SCIMMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

// SCIMGroup is a SCIM Group resource (a team).
type SCIMGroup struct {
	Schemas     []string      `json:"schemas"`
	ID          string        `json:"id"`
	ExternalID  *string       `json:"externalId,omitempty"`
	DisplayName string        `json:"displayName"`
	Members     *[]SCIMMember `json:"members,omitempty"`
	Meta        SCIMMeta      `json:"meta"`
}

type SCIMListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int64    `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

type SCIMErrorResponse struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail"`
}

// SCIMTokenRequest names a new SCIM token.
type SCIMTokenRequest struct {
	Name string `json:"name"`
}

type scimUserInput struct {
	UserName    *string         `json:"userName"`
	ExternalID  *string         `json:"externalId"`
	DisplayName *string         `json:"displayName"`
	Name        *scimNameInput  `json:"name"`
	Emails      []SCIMEmail     `json:"emails"`
	Active      json.RawMessage `json:"active"`
}

type scimNameInput struct {
	Formatted  *string `json:"formatted"`
	GivenName  *string `json:"givenName"`
	FamilyName *string `json:"familyName"`
}

type scimGroupInput struct {
	DisplayName *string          `json:"displayName"`
	ExternalID  *string          `json:"externalId"`
	Members     *json.RawMessage `json:"members"`
}

type scimPatch struct {
	Operations []scimPatchOp `json:"Operations"`
}

type scimPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// RegisterSCIMRoutes mounts SCIM 2.0 at /scim/v2, authenticated by an
// organization's SCIM token rather than a session.
func (c *Controller) RegisterSCIMRoutes(router *gin.RouterGroup) {
	router.Use(c.scimAuth)
	router.GET("/ServiceProviderConfig", c.SCIMServiceProviderConfig)
	router.GET("/ResourceTypes", c.SCIMResourceTypes)
	router.GET("/Schemas", c.SCIMSchemas)
	router.GET("/Users", c.SCIMListUsers)
	router.POST("/Users", c.SCIMCreateUser)
	router.GET("/Users/:id", c.SCIMGetUser)
	router.PUT("/Users/:id", c.SCIMReplaceUser)
	router.PATCH("/Users/:id", c.SCIMPatchUser)
	router.DELETE("/Users/:id", c.SCIMDeleteUser)
	router.GET("/Groups", c.SCIMListGroups)
	router.POST("/Groups", c.SCIMCreateGroup)
	router.GET("/Groups/:id", c.SCIMGetGroup)
	router.PUT("/Groups/:id", c.SCIMReplaceGroup)
	router.PATCH("/Groups/:id", c.SCIMPatchGroup)
	router.DELETE("/Groups/:id", c.SCIMDeleteGroup)
}

// SCIMServiceProviderConfig
// @Summary SCIM service provider configuration
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]any
// @Router /scim/v2/ServiceProviderConfig [get]
func (c *Controller) SCIMServiceProviderConfig(ctx *gin.Context) {
	supported := func(on bool) gin.H { return gin.H{"supported": on} }
	scimJSON(ctx, http.StatusOK, gin.H{
		"schemas":          []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":            supported(true),
		"bulk":             gin.H{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           gin.H{"supported": true, "maxResults": scimPageMax},
		"changePassword":   supported(false),
		"sort":             supported(false),
		"etag":             supported(false),
		"documentationUri": "https://github.com/obzly/obzly/blob/main/ARCHITECTURE.md",
		"authenticationSchemes": []gin.H{{
			"type": "oauthbearertoken", "name": "OAuth Bearer Token", "primary": true,
			"description": "An organization's SCIM token (Organizations > SCIM)",
		}},
		"meta": gin.H{"resourceType": "ServiceProviderConfig", "location": scimBase(ctx) + "/ServiceProviderConfig"},
	})
}

// SCIMResourceTypes
// @Summary SCIM resource types
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Success 200 {object} SCIMListResponse
// @Router /scim/v2/ResourceTypes [get]
func (c *Controller) SCIMResourceTypes(ctx *gin.Context) {
	resource := func(name, endpoint, schema string) any {
		return gin.H{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
			"id":       name,
			"name":     name,
			"endpoint": endpoint,
			"schema":   schema,
			"meta":     gin.H{"resourceType": "ResourceType", "location": scimBase(ctx) + "/ResourceTypes/" + name},
		}
	}
	scimJSON(ctx, http.StatusOK, scimList([]any{
		resource("User", "/Users", scimUserSchema),
		resource("Group", "/Groups", scimGroupSchema),
	}, 2, 1))
}

// SCIMSchemas
// @Summary SCIM schemas
// @Description The User and Group attributes Obzly supports
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Success 200 {object} SCIMListResponse
// @Router /scim/v2/Schemas [get]
func (c *Controller) SCIMSchemas(ctx *gin.Context) {
	attr := func(name, kind string, required bool, extra gin.H) gin.H {
		out := gin.H{
			"name": name, "type": kind, "multiValued": false, "required": required, "caseExact": false,
			"mutability": "readWrite", "returned": "default", "uniqueness": "none",
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	schema := func(id, name string, attributes []gin.H) any {
		return gin.H{
			"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"},
			"id":      id, "name": name, "attributes": attributes,
			"meta": gin.H{"resourceType": "Schema", "location": scimBase(ctx) + "/Schemas/" + id},
		}
	}
	sub := func(names ...string) []gin.H {
		out := []gin.H{}
		for _, name := range names {
			out = append(out, attr(name, "string", false, nil))
		}
		return out
	}
	user := schema(scimUserSchema, "User", []gin.H{
		attr("userName", "string", true, gin.H{"uniqueness": "server"}),
		attr("externalId", "string", false, nil),
		attr("displayName", "string", false, nil),
		attr("name", "complex", false, gin.H{"subAttributes": sub("formatted", "givenName", "familyName")}),
		attr("emails", "complex", false, gin.H{"multiValued": true, "subAttributes": sub("value", "type")}),
		attr("active", "boolean", false, nil),
	})
	group := schema(scimGroupSchema, "Group", []gin.H{
		attr("displayName", "string", true, gin.H{"uniqueness": "server"}),
		attr("externalId", "string", false, nil),
		attr("members", "complex", false, gin.H{"multiValued": true, "subAttributes": sub("value", "display")}),
	})
	scimJSON(ctx, http.StatusOK, scimList([]any{user, group}, 2, 1))
}

// SCIMListUsers
// @Summary List SCIM users
// @Description The organization's members. Filters: userName, externalId, id, emails.value (eq)
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Param filter query string false "e.g. userName eq \"a@b.com\""
// @Param startIndex query int false "1-based"
// @Param count query int false "Page size (max 200)"
// @Success 200 {object} SCIMListResponse
// @Failure 400 {object} SCIMErrorResponse
// @Failure 401 {object} SCIMErrorResponse
// @Router /scim/v2/Users [get]
func (c *Controller) SCIMListUsers(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	filter, err := parseSCIMFilter(ctx.Query("filter"), scimUserFilters)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	start, count := scimPage(ctx)
	rows, total, err := c.service.scimListUsers(token.OrgID, filter, start, count)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	resources := make([]any, 0, len(rows))
	for i := range rows {
		resources = append(resources, scimUserResource(ctx, &rows[i]))
	}
	scimJSON(ctx, http.StatusOK, scimList(resources, total, start))
}

// SCIMCreateUser
// @Summary Provision a SCIM user
// @Description Creates an account the organization owns (no password: SSO sign-in). An email that already has an account gets 409: an owner adds it as a member instead
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body SCIMUser true "User"
// @Success 201 {object} SCIMUser
// @Failure 400 {object} SCIMErrorResponse
// @Failure 409 {object} SCIMErrorResponse
// @Router /scim/v2/Users [post]
func (c *Controller) SCIMCreateUser(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	var input scimUserInput
	if !scimBind(ctx, &input) {
		return
	}
	change, err := input.change(true)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	row, err := c.service.scimCreateUser(token, change)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	resource := scimUserResource(ctx, row)
	ctx.Header("Location", resource.Meta.Location)
	scimJSON(ctx, http.StatusCreated, resource)
}

// SCIMGetUser
// @Summary Get a SCIM user
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} SCIMUser
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Users/{id} [get]
func (c *Controller) SCIMGetUser(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "User")
	if !ok {
		return
	}
	row, err := c.service.scimGetUser(token.OrgID, id)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	scimJSON(ctx, http.StatusOK, scimUserResource(ctx, row))
}

// SCIMReplaceUser
// @Summary Replace a SCIM user
// @Description Name and email change only on accounts the organization's SCIM created
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param request body SCIMUser true "User"
// @Success 200 {object} SCIMUser
// @Failure 400 {object} SCIMErrorResponse
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Users/{id} [put]
func (c *Controller) SCIMReplaceUser(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "User")
	if !ok {
		return
	}
	var input scimUserInput
	if !scimBind(ctx, &input) {
		return
	}
	change, err := input.change(false)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	c.respondUser(ctx, token, id, change)
}

// SCIMPatchUser
// @Summary Patch a SCIM user
// @Description Supports active, userName, displayName, name.*, externalId; other attributes are ignored
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param request body map[string]any true "PatchOp"
// @Success 200 {object} SCIMUser
// @Failure 400 {object} SCIMErrorResponse
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Users/{id} [patch]
func (c *Controller) SCIMPatchUser(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "User")
	if !ok {
		return
	}
	var patch scimPatch
	if !scimBind(ctx, &patch) {
		return
	}
	change, err := patch.userChange()
	if err != nil {
		scimFail(ctx, err)
		return
	}
	c.respondUser(ctx, token, id, change)
}

// SCIMDeleteUser
// @Summary Deprovision a SCIM user
// @Description Removes the member; an account the organization's SCIM created is also deactivated
// @Tags scim
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 204
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Users/{id} [delete]
func (c *Controller) SCIMDeleteUser(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "User")
	if !ok {
		return
	}
	if err := c.service.scimDeleteUser(token, id); err != nil {
		scimFail(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// SCIMListGroups
// @Summary List SCIM groups
// @Description The organization's teams. Filters: displayName, externalId, id (eq); excludedAttributes=members
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Param filter query string false "e.g. displayName eq \"SRE\""
// @Param startIndex query int false "1-based"
// @Param count query int false "Page size (max 200)"
// @Param excludedAttributes query string false "members"
// @Success 200 {object} SCIMListResponse
// @Failure 400 {object} SCIMErrorResponse
// @Router /scim/v2/Groups [get]
func (c *Controller) SCIMListGroups(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	filter, err := parseSCIMFilter(ctx.Query("filter"), scimGroupFilters)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	start, count := scimPage(ctx)
	teams, total, err := c.service.scimListGroups(token.OrgID, filter, start, count)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	resources := make([]any, 0, len(teams))
	for _, team := range teams {
		resources = append(resources, scimGroupResource(ctx, team))
	}
	scimJSON(ctx, http.StatusOK, scimList(resources, total, start))
}

// SCIMCreateGroup
// @Summary Create a SCIM group (team)
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body SCIMGroup true "Group"
// @Success 201 {object} SCIMGroup
// @Failure 400 {object} SCIMErrorResponse
// @Failure 409 {object} SCIMErrorResponse
// @Router /scim/v2/Groups [post]
func (c *Controller) SCIMCreateGroup(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	var input scimGroupInput
	if !scimBind(ctx, &input) {
		return
	}
	change, err := input.change()
	if err != nil {
		scimFail(ctx, err)
		return
	}
	team, err := c.service.scimCreateGroup(token, change)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	resource := scimGroupResource(ctx, team)
	ctx.Header("Location", resource.Meta.Location)
	scimJSON(ctx, http.StatusCreated, resource)
}

// SCIMGetGroup
// @Summary Get a SCIM group
// @Tags scim
// @Produce json
// @Security BearerAuth
// @Param id path string true "Team ID"
// @Param excludedAttributes query string false "members"
// @Success 200 {object} SCIMGroup
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Groups/{id} [get]
func (c *Controller) SCIMGetGroup(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "Group")
	if !ok {
		return
	}
	team, err := c.service.scimGetGroup(token.OrgID, id)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	scimJSON(ctx, http.StatusOK, scimGroupResource(ctx, team))
}

// SCIMReplaceGroup
// @Summary Replace a SCIM group
// @Description Members, when given, become the team's full member list
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Team ID"
// @Param request body SCIMGroup true "Group"
// @Success 200 {object} SCIMGroup
// @Failure 400 {object} SCIMErrorResponse
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Groups/{id} [put]
func (c *Controller) SCIMReplaceGroup(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "Group")
	if !ok {
		return
	}
	var input scimGroupInput
	if !scimBind(ctx, &input) {
		return
	}
	change, err := input.change()
	if err != nil {
		scimFail(ctx, err)
		return
	}
	c.respondGroup(ctx, token, id, change)
}

// SCIMPatchGroup
// @Summary Patch a SCIM group
// @Description Add, remove or replace members; replace displayName or externalId
// @Tags scim
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Team ID"
// @Param request body map[string]any true "PatchOp"
// @Success 200 {object} SCIMGroup
// @Failure 400 {object} SCIMErrorResponse
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Groups/{id} [patch]
func (c *Controller) SCIMPatchGroup(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "Group")
	if !ok {
		return
	}
	var patch scimPatch
	if !scimBind(ctx, &patch) {
		return
	}
	change, err := patch.groupChange()
	if err != nil {
		scimFail(ctx, err)
		return
	}
	c.respondGroup(ctx, token, id, change)
}

// SCIMDeleteGroup
// @Summary Delete a SCIM group
// @Description Deletes the team; its members lose its roles
// @Tags scim
// @Security BearerAuth
// @Param id path string true "Team ID"
// @Success 204
// @Failure 404 {object} SCIMErrorResponse
// @Router /scim/v2/Groups/{id} [delete]
func (c *Controller) SCIMDeleteGroup(ctx *gin.Context) {
	token := scimTokenOf(ctx)
	id, ok := scimID(ctx, "Group")
	if !ok {
		return
	}
	if err := c.service.scimDeleteGroup(token, id); err != nil {
		scimFail(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

func (c *Controller) scimAuth(ctx *gin.Context) {
	token, err := c.service.authenticateSCIM(ctx.GetHeader("Authorization"))
	if err != nil {
		ctx.Header("WWW-Authenticate", `Bearer realm="scim"`)
		scimFail(ctx, &scimError{http.StatusUnauthorized, "", "Invalid or missing SCIM token"})
		ctx.Abort()
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, scimBodyLimit)
	ctx.Set(scimTokenKey, token)
	ctx.Next()
}

func (c *Controller) respondUser(ctx *gin.Context, token *SCIMToken, id uuid.UUID, change scimUserChange) {
	row, err := c.service.scimUpdateUser(token, id, change)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	scimJSON(ctx, http.StatusOK, scimUserResource(ctx, row))
}

func (c *Controller) respondGroup(ctx *gin.Context, token *SCIMToken, id uuid.UUID, change scimGroupChange) {
	team, err := c.service.scimUpdateGroup(token, id, change)
	if err != nil {
		scimFail(ctx, err)
		return
	}
	scimJSON(ctx, http.StatusOK, scimGroupResource(ctx, team))
}

func (in *scimUserInput) change(create bool) (scimUserChange, error) {
	change := scimUserChange{externalID: in.ExternalID}
	if in.UserName != nil {
		change.email = in.UserName
	}
	// Emails stand in for a userName that isn't one, at creation only:
	// afterwards the sign-in email follows userName.
	if create && (change.email == nil || !strings.Contains(*change.email, "@")) {
		for _, email := range in.Emails {
			if email.Primary || change.email == nil || !strings.Contains(*change.email, "@") {
				value := email.Value
				change.email = &value
				if email.Primary {
					break
				}
			}
		}
	}
	if in.DisplayName != nil {
		change.name = in.DisplayName
	}
	if in.Name != nil {
		if change.name == nil && in.Name.Formatted != nil {
			change.name = in.Name.Formatted
		}
		change.givenName, change.familyName = in.Name.GivenName, in.Name.FamilyName
	}
	if len(in.Active) > 0 {
		active, err := scimBool(in.Active)
		if err != nil {
			return change, err
		}
		change.active = &active
	}
	return change, nil
}

func (in *scimGroupInput) change() (scimGroupChange, error) {
	change := scimGroupChange{name: in.DisplayName, externalID: in.ExternalID}
	if in.Members != nil {
		members, err := scimMemberIDs(*in.Members)
		if err != nil {
			return change, err
		}
		change.replace, change.members = true, members
	}
	return change, nil
}

func (p *scimPatch) userChange() (scimUserChange, error) {
	change := scimUserChange{}
	for _, op := range p.Operations {
		kind, err := scimOpKind(op.Op)
		if err != nil {
			return change, err
		}
		if kind == "remove" {
			continue
		}
		if op.Path == "" {
			var values map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &values); err != nil {
				return change, &scimError{400, "invalidValue", "A patch without a path needs an object value"}
			}
			for key, value := range values {
				if err := applySCIMUserAttribute(&change, key, value); err != nil {
					return change, err
				}
			}
			continue
		}
		if err := applySCIMUserAttribute(&change, op.Path, op.Value); err != nil {
			return change, err
		}
	}
	return change, nil
}

func (p *scimPatch) groupChange() (scimGroupChange, error) {
	change := scimGroupChange{}
	for _, op := range p.Operations {
		kind, err := scimOpKind(op.Op)
		if err != nil {
			return change, err
		}
		path := strings.ToLower(strings.TrimSpace(op.Path))
		switch {
		case path == "":
			var values map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &values); err != nil {
				return change, &scimError{400, "invalidValue", "A patch without a path needs an object value"}
			}
			for key, value := range values {
				switch strings.ToLower(key) {
				case "displayname":
					if err := scimString(value, &change.name); err != nil {
						return change, err
					}
				case "externalid":
					if err := scimString(value, &change.externalID); err != nil {
						return change, err
					}
				case "members":
					if err := applySCIMMembers(&change, kind, value); err != nil {
						return change, err
					}
				}
			}
		case path == "displayname" && kind != "remove":
			if err := scimString(op.Value, &change.name); err != nil {
				return change, err
			}
		case path == "externalid":
			if kind == "remove" {
				empty := ""
				change.externalID = &empty
			} else if err := scimString(op.Value, &change.externalID); err != nil {
				return change, err
			}
		case path == "members":
			if err := applySCIMMembers(&change, kind, op.Value); err != nil {
				return change, err
			}
		case strings.HasPrefix(path, "members[") && strings.HasSuffix(path, "]") && kind == "remove":
			raw := strings.TrimSpace(op.Path)
			filter, err := parseSCIMFilter(raw[len("members["):len(raw)-1], map[string]string{"value": "value"})
			if err != nil || filter == nil {
				return change, &scimError{400, "invalidPath", "Unsupported members path"}
			}
			id, err := uuid.Parse(filter.value)
			if err != nil {
				return change, &scimError{400, "invalidValue", "Member values are user ids"}
			}
			change.remove = append(change.remove, id)
		default:
			return change, &scimError{400, "invalidPath", "Unsupported path: " + op.Path}
		}
	}
	return change, nil
}

// applySCIMMembers folds one members operation into change; a replace
// after adds and removes starts over from its set.
func applySCIMMembers(change *scimGroupChange, kind string, value json.RawMessage) error {
	if kind == "remove" && (len(value) == 0 || string(value) == "null") {
		change.replace, change.members, change.add, change.remove = true, nil, nil, nil
		return nil
	}
	ids, err := scimMemberIDs(value)
	if err != nil {
		return err
	}
	switch kind {
	case "add":
		if change.replace {
			change.members = append(change.members, ids...)
		} else {
			change.add = append(change.add, ids...)
		}
	case "remove":
		if change.replace {
			change.members = slicesWithout(change.members, ids)
		} else {
			change.remove = append(change.remove, ids...)
		}
	default:
		change.replace, change.members, change.add, change.remove = true, ids, nil, nil
	}
	return nil
}

func applySCIMUserAttribute(change *scimUserChange, path string, value json.RawMessage) error {
	switch key := strings.ToLower(strings.TrimSpace(path)); key {
	case "active":
		active, err := scimBool(value)
		if err != nil {
			return err
		}
		change.active = &active
	case "username":
		return scimString(value, &change.email)
	case "displayname", "name.formatted":
		return scimString(value, &change.name)
	case "name.givenname":
		return scimString(value, &change.givenName)
	case "name.familyname":
		return scimString(value, &change.familyName)
	case "externalid":
		return scimString(value, &change.externalID)
	case "name":
		var name scimNameInput
		if err := json.Unmarshal(value, &name); err != nil {
			return &scimError{400, "invalidValue", "name must be an object"}
		}
		if name.Formatted != nil {
			change.name = name.Formatted
		}
		change.givenName, change.familyName = name.GivenName, name.FamilyName
	}
	// Anything else (emails, title, enterprise extension...) isn't stored.
	return nil
}

func scimOpKind(op string) (string, error) {
	switch kind := strings.ToLower(strings.TrimSpace(op)); kind {
	case "add", "replace", "remove":
		return kind, nil
	default:
		return "", &scimError{400, "invalidSyntax", "Unknown patch op: " + op}
	}
}

func scimMemberIDs(raw json.RawMessage) ([]uuid.UUID, error) {
	var members []SCIMMember
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, &scimError{400, "invalidValue", "members must be a list of {value}"}
	}
	ids := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		id, err := uuid.Parse(member.Value)
		if err != nil {
			return nil, &scimError{400, "invalidValue", "Member values are user ids"}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// scimBool accepts true/false and Entra's "True"/"False".
func scimBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if parsed, err := strconv.ParseBool(strings.ToLower(s)); err == nil {
			return parsed, nil
		}
	}
	return false, &scimError{400, "invalidValue", "active must be a boolean"}
}

func scimString(raw json.RawMessage, target **string) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return &scimError{400, "invalidValue", "Expected a string"}
	}
	*target = &s
	return nil
}

var (
	scimUserFilters = map[string]string{
		"username":                     "lower(u.email)",
		"emails.value":                 "lower(u.email)",
		`emails[type eq "work"].value`: "lower(u.email)",
		"externalid":                   "m.external_id",
		"id":                           "u.id::text",
	}
	scimGroupFilters = map[string]string{
		"displayname": "lower(t.name)",
		"externalid":  "t.external_id",
		"id":          "t.id::text",
	}
)

// parseSCIMFilter parses `attribute eq "value"` - the form Okta and Entra
// send - against an allowlist of attributes.
func parseSCIMFilter(filter string, columns map[string]string) (*scimFilter, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return nil, nil
	}
	invalid := &scimError{400, "invalidFilter", "Supported filters: <attribute> eq \"value\""}
	depth, quoted, end := 0, false, len(filter)
	for i, r := range filter {
		switch {
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == ' ' && depth == 0:
			end = i
		}
		if end != len(filter) {
			break
		}
	}
	attribute := strings.ToLower(filter[:end])
	op, value, ok := strings.Cut(strings.TrimSpace(filter[end:]), " ")
	if !ok || !strings.EqualFold(op, "eq") {
		return nil, invalid
	}
	column, ok := columns[attribute]
	if !ok {
		return nil, invalid
	}
	var parsed string
	if err := json.Unmarshal([]byte(strings.TrimSpace(value)), &parsed); err != nil {
		return nil, invalid
	}
	if strings.HasPrefix(column, "lower(") {
		parsed = strings.ToLower(parsed)
	}
	return &scimFilter{column: column, value: parsed}, nil
}

func scimUserResource(ctx *gin.Context, row *scimUserRow) *SCIMUser {
	given, family, _ := strings.Cut(row.Name, " ")
	created := row.CreatedAt
	return &SCIMUser{
		Schemas:     []string{scimUserSchema},
		ID:          row.UserID.String(),
		ExternalID:  row.ExternalID,
		UserName:    row.Email,
		Name:        SCIMName{Formatted: row.Name, GivenName: given, FamilyName: family},
		DisplayName: row.Name,
		Emails:      []SCIMEmail{{Value: row.Email, Type: "work", Primary: true}},
		Active:      row.active(),
		Meta: SCIMMeta{
			ResourceType: "User",
			Created:      &created,
			Location:     scimBase(ctx) + "/Users/" + row.UserID.String(),
		},
	}
}

func scimGroupResource(ctx *gin.Context, team *Team) *SCIMGroup {
	created := team.CreatedAt
	group := &SCIMGroup{
		Schemas:     []string{scimGroupSchema},
		ID:          team.ID.String(),
		ExternalID:  team.ExternalID,
		DisplayName: team.Name,
		Meta: SCIMMeta{
			ResourceType: "Group",
			Created:      &created,
			Location:     scimBase(ctx) + "/Groups/" + team.ID.String(),
		},
	}
	if !strings.Contains(strings.ToLower(ctx.Query("excludedAttributes")), "members") {
		members := make([]SCIMMember, 0, len(team.Members))
		for _, member := range team.Members {
			members = append(members, SCIMMember{
				Value:   member.UserID.String(),
				Display: member.Email,
				Ref:     scimBase(ctx) + "/Users/" + member.UserID.String(),
			})
		}
		group.Members = &members
	}
	return group
}

func scimList(resources []any, total int64, start int) *SCIMListResponse {
	return &SCIMListResponse{
		Schemas: []string{scimListSchema}, TotalResults: total, StartIndex: start,
		ItemsPerPage: len(resources), Resources: resources,
	}
}

func scimPage(ctx *gin.Context) (int, int) {
	start, err := strconv.Atoi(ctx.Query("startIndex"))
	if err != nil || start < 1 {
		start = 1
	}
	count, err := strconv.Atoi(ctx.Query("count"))
	if err != nil {
		count = 100
	}
	return start, max(0, min(count, scimPageMax))
}

func scimID(ctx *gin.Context, kind string) (uuid.UUID, bool) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		scimFail(ctx, &scimError{http.StatusNotFound, "", kind + " not found"})
		return uuid.Nil, false
	}
	return id, true
}

func scimBind(ctx *gin.Context, target any) bool {
	if err := json.NewDecoder(ctx.Request.Body).Decode(target); err != nil {
		scimFail(ctx, &scimError{http.StatusBadRequest, "invalidSyntax", "Invalid JSON body"})
		return false
	}
	return true
}

func scimBase(ctx *gin.Context) string {
	scheme := ctx.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if ctx.Request.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + ctx.Request.Host + "/scim/v2"
}

func scimJSON(ctx *gin.Context, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		ctx.Status(http.StatusInternalServerError)
		return
	}
	ctx.Data(status, scimContentType, encoded)
}

func scimFail(ctx *gin.Context, err error) {
	var known *scimError
	if !errors.As(err, &known) {
		logger.GetLogger().Error("SCIM request failed", "path", ctx.FullPath(), "error", err)
		known = &scimError{http.StatusInternalServerError, "", "Internal error"}
	}
	scimJSON(ctx, known.status, &SCIMErrorResponse{
		Schemas: []string{
			scimErrorSchema,
		},
		Status:   strconv.Itoa(known.status),
		ScimType: known.scimType,
		Detail:   known.detail,
	})
}

func slicesWithout(ids, drop []uuid.UUID) []uuid.UUID {
	out := ids[:0:0]
	for _, id := range ids {
		keep := true
		for _, d := range drop {
			keep = keep && id != d
		}
		if keep {
			out = append(out, id)
		}
	}
	return out
}

// scimTokenOf is the token scimAuth authenticated.
func scimTokenOf(ctx *gin.Context) *SCIMToken {
	token, _ := ctx.MustGet(scimTokenKey).(*SCIMToken)
	return token
}
