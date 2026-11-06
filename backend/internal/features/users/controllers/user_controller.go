package users_controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"
	user_dto "obzly/internal/features/users/dto"
	mfa_services "obzly/internal/features/users/mfa/services"
	user_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_services "obzly/internal/features/users/sso/services"
	"obzly/internal/util/logger"
	"obzly/internal/util/publicurl"

	"github.com/gin-gonic/gin"
)

type UserController struct {
	userService     *users_services.UserService
	settingsService *users_services.SettingsService
}

func (c *UserController) RegisterRoutes(router *gin.RouterGroup) {
	router.POST("/users/signup", c.SignUp)
	router.POST("/users/signin", c.SignIn)

	// Admin password setup (no auth required)
	router.GET("/users/admin/has-password", c.IsAdminHasPassword)
	router.POST("/users/admin/set-password", c.SetAdminPassword)

	// OAuth callbacks
	router.GET("/auth/oauth/state", c.GenerateOAuthState)
	router.POST("/auth/github/callback", c.HandleGitHubOAuth)
	router.POST("/auth/google/callback", c.HandleGoogleOAuth)

	// Portal-configured SSO, alongside the fixed GitHub/Google flow.
	router.GET("/auth/sso/connectors", c.ListSSOConnectors)
	router.POST("/auth/sso/callback", c.HandleSSOCallback)

	// Multi-site token exchange, called server-to-server by a control plane. Public:
	// the assertion itself is what's verified.
	router.POST("/auth/site/exchange", c.ExchangeCentralAssertion)
}

func (c *UserController) RegisterProtectedRoutes(router *gin.RouterGroup) {
	router.GET("/users/me", c.GetCurrentUser)
	router.PUT("/users/me", c.UpdateUserInfo)
	router.PUT("/users/change-password", c.ChangePassword)
	router.POST("/users/invite", c.InviteUser)
	router.POST("/users/logout", c.SignOut)
}

// SignUp
// @Summary Register a new user
// @Description Register a new user with email and password
// @Tags users
// @Accept json
// @Produce json
// @Param request body users_dto.SignUpRequestDTO true "User signup data"
// @Success 200
// @Failure 400
// @Failure 429 {object} map[string]string "Rate limit exceeded"
// @Router /users/signup [post]
func (c *UserController) SignUp(ctx *gin.Context) {
	var request user_dto.SignUpRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	err := c.userService.SignUp(&request, ctx.ClientIP())
	if err != nil {
		if errors.Is(err, users_services.ErrSignUpRateLimited) {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "User created successfully"})
}

// SignIn
// @Summary Authenticate a user
// @Description Authenticate a user with email and password
// @Tags users
// @Accept json
// @Produce json
// @Param request body users_dto.SignInRequestDTO true "User signin data"
// @Success 200 {object} users_dto.SignInResponseDTO
// @Failure 400
// @Failure 429 {object} map[string]string "Rate limit exceeded"
// @Router /users/signin [post]
func (c *UserController) SignIn(ctx *gin.Context) {
	var request user_dto.SignInRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.SignIn(&request, ctx.ClientIP())
	if err != nil {
		if errors.Is(err, users_services.ErrLoginRateLimited) {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// SignOut
// @Summary Log out
// @Description Revokes the presented access token so it can no longer be used - see users_services.UserService.SignOut
// @Tags users
// @Produce json
// @Security BearerAuth
// @Success 200
// @Router /users/logout [post]
func (c *UserController) SignOut(ctx *gin.Context) {
	token := ctx.GetHeader("Authorization")
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}

	if err := c.userService.SignOut(token); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log out"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Logged out successfully"})
}

// Admin password endpoints
func (c *UserController) IsAdminHasPassword(ctx *gin.Context) {
	hasPassword, err := c.userService.IsRootAdminHasPassword()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check admin password status"})
		return
	}

	settings, err := c.settingsService.GetSettings()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check registration status"})
		return
	}

	response := user_dto.IsAdminHasPasswordResponseDTO{
		HasPassword:           hasPassword,
		IsRegistrationEnabled: settings.IsAllowExternalRegistrations,
	}
	if !hasPassword {
		response.RootAdminLogin = config.GetEnv().EffectiveRootAdminEmail()
	}
	ctx.JSON(http.StatusOK, response)
}

func (c *UserController) SetAdminPassword(ctx *gin.Context) {
	var request user_dto.SetAdminPasswordRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.userService.SetRootAdminPassword(request.Password, request.BootstrapToken); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Admin password set successfully"})
}

// ChangePassword
// @Summary Change user password
// @Description Change the password for the currently authenticated user
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body users_dto.ChangePasswordRequestDTO true "New password data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /users/change-password [put]
func (c *UserController) ChangePassword(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request user_dto.ChangePasswordRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if request.NewPassword == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "New password is required"})
		return
	}

	if len(request.NewPassword) < 8 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "New password must be at least 8 characters long"})
		return
	}

	err := c.userService.ChangeUserPassword(user.ID, request.CurrentPassword, request.NewPassword, request.MFACode)
	if err != nil {
		switch {
		case errors.Is(err, users_services.ErrCurrentPasswordIncorrect):
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		case errors.Is(err, users_services.ErrMFACodeRequired), errors.Is(err, users_services.ErrMFACodeInvalid):
			ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		case errors.Is(err, mfa_services.ErrTooManyAttempts):
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		default:
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "Password changed successfully"})
}

// InviteUser
// @Summary Invite a new user
// @Description Invite a new user to the system with optional project assignment
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body users_dto.InviteUserRequestDTO true "User invitation data"
// @Success 200 {object} users_dto.InviteUserResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /users/invite [post]
func (c *UserController) InviteUser(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request user_dto.InviteUserRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.InviteUser(&request, user)
	if err != nil {
		if err.Error() == "insufficient permissions to invite users" {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	response.EmailSent = sendInviteEmail(ctx, user.Name, response)

	ctx.JSON(http.StatusOK, response)
}

// sendInviteEmail mails the invite link when email is set up; the inviter
// still sees the link either way.
func sendInviteEmail(ctx *gin.Context, inviter string, invite *user_dto.InviteUserResponseDTO) bool {
	if relay, err := sharedconfig.CurrentSMTP(); err != nil || relay == nil {
		return false
	}
	link := fmt.Sprintf("%s/?invite=%s&email=%s", publicurl.BaseURL(ctx.Request),
		url.QueryEscape(invite.InviteToken), url.QueryEscape(invite.Email))
	body := fmt.Sprintf("%s invited you to Obzly.\n\nSet up your account: %s\n\nThe link works once.", inviter, link)
	if err := sharedconfig.SendMail(
		ctx.Request.Context(),
		[]string{invite.Email},
		"You're invited to Obzly",
		body,
	); err != nil {
		logger.GetLogger().Warn("Failed to email an invite; the inviter can share the link", "error", err)
		return false
	}
	return true
}

// GetCurrentUser
// @Summary Get current user profile
// @Description Get the profile information of the currently authenticated user
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} users_dto.UserProfileResponseDTO
// @Failure 401 {object} map[string]string
// @Router /users/me [get]
func (c *UserController) GetCurrentUser(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	profile := c.userService.GetCurrentUserProfile(user)
	ctx.JSON(http.StatusOK, profile)
}

// UpdateUserInfo
// @Summary Update current user information
// @Description Update name and/or email for the currently authenticated user
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body users_dto.UpdateUserInfoRequestDTO true "User info update data"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 422 {object} map[string]string "MFA code required or invalid"
// @Failure 429 {object} map[string]string
// @Router /users/me [put]
func (c *UserController) UpdateUserInfo(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request user_dto.UpdateUserInfoRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if request.Name == nil && request.Email == nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}

	if err := c.userService.UpdateUserInfo(user.ID, &request); err != nil {
		switch {
		case errors.Is(err, users_services.ErrCurrentPasswordIncorrect):
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		case errors.Is(err, users_services.ErrMFACodeRequired), errors.Is(err, users_services.ErrMFACodeInvalid):
			ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		case errors.Is(err, mfa_services.ErrTooManyAttempts):
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		default:
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "User info updated successfully"})
}

// GenerateOAuthState
// @Summary Mint a signed CSRF state token for the legacy GitHub/Google OAuth flow
// @Description Called by the frontend right before redirecting to the provider - see oauth_state.go's doc comment
// @Tags auth
// @Produce json
// @Param provider query string true "github or google"
// @Success 200 {object} users_dto.GenerateOAuthStateResponseDTO
// @Failure 400 {object} map[string]string
// @Router /auth/oauth/state [get]
func (c *UserController) GenerateOAuthState(ctx *gin.Context) {
	provider := ctx.Query("provider")

	state, err := c.userService.GenerateOAuthState(provider)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, user_dto.GenerateOAuthStateResponseDTO{State: state})
}

// HandleGitHubOAuth
// @Summary Handle GitHub OAuth callback
// @Description Exchange GitHub authorization code for JWT token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body users_dto.OAuthCallbackRequestDTO true "OAuth callback data"
// @Success 200 {object} users_dto.OAuthCallbackResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 501 {object} map[string]string
// @Router /auth/github/callback [post]
func (c *UserController) HandleGitHubOAuth(ctx *gin.Context) {
	env := config.GetEnv()
	if env.GitHubClientID == "" || env.GitHubClientSecret == "" {
		ctx.JSON(http.StatusNotImplemented, gin.H{"error": "GitHub OAuth is not configured"})
		return
	}

	var request user_dto.OAuthCallbackRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.HandleGitHubOAuth(request.Code, request.RedirectUri, request.State)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// HandleGoogleOAuth
// @Summary Handle Google OAuth callback
// @Description Exchange Google authorization code for JWT token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body users_dto.OAuthCallbackRequestDTO true "OAuth callback data"
// @Success 200 {object} users_dto.OAuthCallbackResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 501 {object} map[string]string
// @Router /auth/google/callback [post]
func (c *UserController) HandleGoogleOAuth(ctx *gin.Context) {
	env := config.GetEnv()
	if env.GoogleClientID == "" || env.GoogleClientSecret == "" {
		ctx.JSON(http.StatusNotImplemented, gin.H{"error": "Google OAuth is not configured"})
		return
	}

	var request user_dto.OAuthCallbackRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.HandleGoogleOAuth(request.Code, request.RedirectUri, request.State)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ListSSOConnectors
// @Summary List enabled SSO connectors for the login page
// @Description Public endpoint - every enabled, portal-configured OIDC SSO connector, each with a ready-to-use authorization URL
// @Tags auth
// @Produce json
// @Param redirectUri query string true "The URI the identity provider should redirect back to after sign-in"
// @Success 200 {object} sso_dto.ListPublicConnectorsResponseDTO
// @Failure 400 {object} map[string]string
// @Router /auth/sso/connectors [get]
func (c *UserController) ListSSOConnectors(ctx *gin.Context) {
	redirectUri := ctx.Query("redirectUri")
	if redirectUri == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "redirectUri is required"})
		return
	}

	response, err := sso_services.GetConnectorService().ListPublicConnectors(redirectUri)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// HandleSSOCallback
// @Summary Handle a portal-configured SSO connector's callback
// @Description Exchange an authorization code for a JWT token via the connector named in the signed state parameter
// @Tags auth
// @Accept json
// @Produce json
// @Param request body sso_dto.SSOCallbackRequestDTO true "SSO callback data"
// @Success 200 {object} users_dto.OAuthCallbackResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 429 {object} map[string]string "Rate limit exceeded"
// @Router /auth/sso/callback [post]
func (c *UserController) HandleSSOCallback(ctx *gin.Context) {
	var request sso_dto.SSOCallbackRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.HandleSSOCallback(&request, ctx.ClientIP())
	if err != nil {
		if errors.Is(err, users_services.ErrLoginRateLimited) {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// ExchangeCentralAssertion
// @Summary Exchange a control-plane assertion for a Site session
// @Description Server-to-server only - see users_services.UserService.ExchangeCentralAssertion
// @Tags auth
// @Accept json
// @Produce json
// @Param request body users_dto.SiteExchangeRequestDTO true "Signed assertion"
// @Success 200 {object} users_dto.SignInResponseDTO
// @Failure 400
// @Failure 403
// @Failure 404
// @Failure 409
// @Failure 429
// @Router /auth/site/exchange [post]
func (c *UserController) ExchangeCentralAssertion(ctx *gin.Context) {
	var request user_dto.SiteExchangeRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.userService.ExchangeCentralAssertion(request.Assertion, ctx.ClientIP())
	if err != nil {
		switch {
		case errors.Is(err, users_services.ErrCentralExchangeDisabled):
			// 404, not 400/403: a standalone install should look like it has no such endpoint.
			ctx.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		case errors.Is(err, users_services.ErrLoginRateLimited):
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		case errors.Is(err, users_services.ErrCentralExchangeForbidden),
			errors.Is(err, users_services.ErrCentralExchangeAccountNotLinkable):
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, users_services.ErrCentralExchangeReplayed):
			ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	ctx.JSON(http.StatusOK, response)
}
