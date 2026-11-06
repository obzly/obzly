package oauth

import (
	"errors"
	"net/http"

	access_tokens "obzly/internal/features/access_tokens"
	users_middleware "obzly/internal/features/users/middleware"
	"obzly/internal/util/publicurl"
	"obzly/internal/util/rate_limit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type OAuthController struct {
	service     *OAuthService
	rateLimiter *rate_limit.RateLimiter
}

// RegisterPublicRoutes mounts discovery, token and registration endpoints
// at the server root (RFC 8414 / 9728 locations).
func (c *OAuthController) RegisterPublicRoutes(r *gin.Engine) {
	r.GET("/.well-known/oauth-protected-resource", c.ProtectedResourceMetadata)
	r.GET("/.well-known/oauth-protected-resource/mcp", c.ProtectedResourceMetadata)
	r.GET("/.well-known/oauth-authorization-server", c.AuthorizationServerMetadata)

	for _, path := range []string{"/oauth/token", "/oauth/register"} {
		r.OPTIONS(path, preflight)
	}
	r.POST("/oauth/token", c.Token)
	r.POST("/oauth/register", c.Register)
}

// RegisterRoutes mounts the consent page API and connected apps (signed-in users).
func (c *OAuthController) RegisterRoutes(router *gin.RouterGroup) {
	router.POST("/oauth/authorize/validate", c.ValidateAuthorize)
	router.POST("/oauth/authorize/decision", c.Decide)
	router.GET("/oauth/connected-apps", c.ListConnectedApps)
	router.DELETE("/oauth/connected-apps/:id", c.RevokeConnectedApp)
}

// ProtectedResourceMetadata
// @Summary OAuth protected resource metadata for /mcp (RFC 9728)
// @Tags oauth
// @Produce json
// @Success 200 {object} map[string]any
// @Router /.well-known/oauth-protected-resource/mcp [get]
func (c *OAuthController) ProtectedResourceMetadata(ctx *gin.Context) {
	corsHeaders(ctx)
	base := publicurl.BaseURL(ctx.Request)
	ctx.JSON(http.StatusOK, gin.H{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{access_tokens.ScopeMCPRead},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Obzly",
	})
}

// AuthorizationServerMetadata
// @Summary OAuth authorization server metadata (RFC 8414)
// @Tags oauth
// @Produce json
// @Success 200 {object} map[string]any
// @Router /.well-known/oauth-authorization-server [get]
func (c *OAuthController) AuthorizationServerMetadata(ctx *gin.Context) {
	corsHeaders(ctx)
	base := publicurl.BaseURL(ctx.Request)
	ctx.JSON(http.StatusOK, gin.H{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"scopes_supported":                               []string{access_tokens.ScopeMCPRead},
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

// Token
// @Summary OAuth token endpoint (authorization_code with PKCE, refresh_token)
// @Tags oauth
// @Accept x-www-form-urlencoded
// @Produce json
// @Success 200 {object} TokenResponse
// @Failure 400 {object} TokenError
// @Router /oauth/token [post]
func (c *OAuthController) Token(ctx *gin.Context) {
	corsHeaders(ctx)
	ctx.Header("Cache-Control", "no-store")
	if !c.allow(ctx, 60) {
		return
	}

	var response *TokenResponse
	var tokenErr *TokenError
	switch ctx.PostForm("grant_type") {
	case "authorization_code":
		response, tokenErr = c.service.ExchangeCode(ctx.Request.Context(), ctx.PostForm("client_id"),
			ctx.PostForm("code"), ctx.PostForm("redirect_uri"), ctx.PostForm("code_verifier"), ctx.PostForm("resource"))
	case "refresh_token":
		response, tokenErr = c.service.Refresh(ctx.PostForm("client_id"), ctx.PostForm("refresh_token"),
			ctx.PostForm("resource"))
	default:
		tokenErr = &TokenError{"unsupported_grant_type", "grant_type must be authorization_code or refresh_token"}
	}

	if tokenErr != nil {
		status := http.StatusBadRequest
		if tokenErr.Code == "server_error" {
			status = http.StatusInternalServerError
		}
		ctx.JSON(status, tokenErr)
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// Register
// @Summary OAuth dynamic client registration (RFC 7591, public clients)
// @Tags oauth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Client metadata"
// @Success 201 {object} RegisterResponse
// @Failure 400 {object} TokenError
// @Router /oauth/register [post]
func (c *OAuthController) Register(ctx *gin.Context) {
	corsHeaders(ctx)
	if !c.allow(ctx, 10) {
		return
	}

	var request RegisterRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, TokenError{"invalid_client_metadata", "invalid JSON client metadata"})
		return
	}
	response, tokenErr := c.service.Register(&request)
	if tokenErr != nil {
		ctx.JSON(http.StatusBadRequest, tokenErr)
		return
	}
	ctx.JSON(http.StatusCreated, response)
}

// ValidateAuthorize
// @Summary Check an OAuth authorization request for the consent page
// @Tags oauth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body AuthorizeRequest true "Authorization request parameters"
// @Success 200 {object} ConsentInfo
// @Failure 400 {object} map[string]any
// @Router /oauth/authorize/validate [post]
func (c *OAuthController) ValidateAuthorize(ctx *gin.Context) {
	var request AuthorizeRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorization request", "fatal": true})
		return
	}

	base := publicurl.BaseURL(ctx.Request)
	info, _, authErr := c.service.ValidateAuthorize(ctx.Request.Context(), &request, base, base+"/mcp")
	if authErr != nil {
		body := gin.H{"error": authErr.Description, "fatal": authErr.Fatal}
		if !authErr.Fatal {
			body["redirectTo"] = errorRedirect(&request, base, authErr.Code, authErr.Description)
		}
		ctx.JSON(http.StatusBadRequest, body)
		return
	}
	ctx.JSON(http.StatusOK, info)
}

type decisionRequest struct {
	AuthorizeRequest
	Approve bool `json:"approve"`
}

// Decide
// @Summary Approve or deny an OAuth authorization request
// @Tags oauth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /oauth/authorize/decision [post]
func (c *OAuthController) Decide(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	var request decisionRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorization request", "fatal": true})
		return
	}

	base := publicurl.BaseURL(ctx.Request)
	redirectTo, authErr := c.service.Decide(ctx.Request.Context(), user, &request.AuthorizeRequest,
		request.Approve, base, base+"/mcp", users_middleware.SessionSatisfiesMFA(ctx))
	if authErr != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": authErr.Description, "fatal": true})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"redirectTo": redirectTo})
}

// ListConnectedApps
// @Summary List OAuth apps with access to the current user's MCP data
// @Tags oauth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]any
// @Router /oauth/connected-apps [get]
func (c *OAuthController) ListConnectedApps(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	apps, err := c.service.ListConnectedApps(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list connected apps"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"apps": apps})
}

// RevokeConnectedApp
// @Summary Revoke an OAuth app's access (all its tokens)
// @Tags oauth
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connected app (grant) ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /oauth/connected-apps/{id} [delete]
func (c *OAuthController) RevokeConnectedApp(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid app ID"})
		return
	}
	if err := c.service.RevokeConnectedApp(user, id); err != nil {
		if errors.Is(err, ErrAppNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke access"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Access revoked"})
}

// allow applies a per-IP limit of perMinute requests (bursts allowed).
func (c *OAuthController) allow(ctx *gin.Context, perMinute int) bool {
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("oauth:"+ctx.FullPath()+":"+ctx.ClientIP()))
	result, err := c.rateLimiter.CheckRateLimit(key, max(perMinute/60, 1), perMinute)
	if err == nil && !result.Allowed {
		ctx.JSON(http.StatusTooManyRequests, TokenError{"slow_down", "too many requests, retry shortly"})
		return false
	}
	return true
}

// Public OAuth endpoints are called by browser-based clients too; no cookies
// are involved, so any origin is allowed.
func corsHeaders(ctx *gin.Context) {
	ctx.Header("Access-Control-Allow-Origin", "*")
	ctx.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	ctx.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Protocol-Version")
}

func preflight(ctx *gin.Context) {
	corsHeaders(ctx)
	ctx.Status(http.StatusNoContent)
}
