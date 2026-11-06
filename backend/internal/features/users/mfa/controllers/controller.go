// Package mfa_controllers exposes self-service MFA routes (RegisterProtectedRoutes,
// for signed-in users) and login-time routes (RegisterPublicRoutes, using the
// challenge token instead of an access token).
package mfa_controllers

import (
	"errors"
	"net/http"

	mfa_dto "obzly/internal/features/users/mfa/dto"
	mfa_services "obzly/internal/features/users/mfa/services"
	user_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"

	"github.com/gin-gonic/gin"
)

type MFAController struct {
	mfaService *mfa_services.Service
}

func (c *MFAController) RegisterProtectedRoutes(router *gin.RouterGroup) {
	routes := router.Group("/users/mfa")

	routes.GET("/status", c.GetStatus)
	routes.POST("/setup", c.Setup)
	routes.POST("/enable", c.Enable)
	routes.POST("/disable", c.Disable)
}

func (c *MFAController) RegisterPublicRoutes(router *gin.RouterGroup) {
	routes := router.Group("/users/mfa")

	routes.POST("/login-setup", c.LoginSetup)
	routes.POST("/login-enable", c.LoginEnable)
	routes.POST("/login-verify", c.LoginVerify)
}

// GetStatus
// @Summary Get the current user's MFA status
// @Tags mfa
// @Produce json
// @Security BearerAuth
// @Success 200 {object} mfa_dto.StatusResponseDTO
// @Router /users/mfa/status [get]
func (c *MFAController) GetStatus(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	status := c.mfaService.GetStatus(user)
	// Tells the forced-setup gate this session's second factor was verified
	// elsewhere, so local enrollment isn't demanded.
	status.SatisfiedByIdentityProvider = user_middleware.SessionSatisfiesMFA(ctx)
	ctx.JSON(http.StatusOK, status)
}

// Setup
// @Summary Start MFA setup for the current user
// @Tags mfa
// @Produce json
// @Security BearerAuth
// @Success 200 {object} mfa_dto.SetupResponseDTO
// @Router /users/mfa/setup [post]
func (c *MFAController) Setup(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	response, err := c.mfaService.Setup(user)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// Enable
// @Summary Confirm MFA setup with a code from the authenticator app
// @Tags mfa
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body mfa_dto.EnableRequestDTO true "Code"
// @Success 200 {object} map[string]string
// @Router /users/mfa/enable [post]
func (c *MFAController) Enable(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request mfa_dto.EnableRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.mfaService.Enable(user, request.Code); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "MFA enabled successfully"})
}

// Disable
// @Summary Disable MFA (Member accounts only - refused for Admin)
// @Tags mfa
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body mfa_dto.DisableRequestDTO true "Code"
// @Success 200 {object} map[string]string
// @Router /users/mfa/disable [post]
func (c *MFAController) Disable(ctx *gin.Context) {
	user, ok := user_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request mfa_dto.DisableRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := c.mfaService.Disable(user, request.Code); err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "MFA disabled successfully"})
}

// LoginSetup
// @Summary Start forced MFA setup mid-login, using the challenge token SignIn returned
// @Tags mfa
// @Accept json
// @Produce json
// @Param request body mfa_dto.LoginSetupRequestDTO true "MFA token"
// @Success 200 {object} mfa_dto.SetupResponseDTO
// @Router /users/mfa/login-setup [post]
func (c *MFAController) LoginSetup(ctx *gin.Context) {
	var request mfa_dto.LoginSetupRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	response, err := c.mfaService.SetupWithToken(request.MFAToken)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, response)
}

// LoginEnable
// @Summary Complete forced MFA setup mid-login - returns a real access token on success
// @Tags mfa
// @Accept json
// @Produce json
// @Param request body mfa_dto.LoginEnableRequestDTO true "MFA token + code"
// @Success 200 {object} users_dto.SignInResponseDTO
// @Router /users/mfa/login-enable [post]
func (c *MFAController) LoginEnable(ctx *gin.Context) {
	var request mfa_dto.LoginEnableRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	user, err := c.mfaService.EnableWithToken(request.MFAToken, request.Code)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	response, err := users_services.GetUserService().GenerateAccessToken(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate access token"})
		return
	}
	response.Email = user.Email

	ctx.JSON(http.StatusOK, response)
}

// LoginVerify
// @Summary Complete a login for an account with MFA already enabled - returns a real access token on success
// @Tags mfa
// @Accept json
// @Produce json
// @Param request body mfa_dto.LoginVerifyRequestDTO true "MFA token + code"
// @Success 200 {object} users_dto.SignInResponseDTO
// @Router /users/mfa/login-verify [post]
func (c *MFAController) LoginVerify(ctx *gin.Context) {
	var request mfa_dto.LoginVerifyRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	user, err := c.mfaService.VerifyLoginToken(request.MFAToken, request.Code)
	if err != nil {
		c.handleError(ctx, err)
		return
	}

	response, err := users_services.GetUserService().GenerateAccessToken(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate access token"})
		return
	}
	response.Email = user.Email

	ctx.JSON(http.StatusOK, response)
}

func (c *MFAController) handleError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, mfa_services.ErrAlreadyEnabled),
		errors.Is(err, mfa_services.ErrNotSetUp),
		errors.Is(err, mfa_services.ErrNotEnabled),
		errors.Is(err, mfa_services.ErrRequiredForAdmin):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, mfa_services.ErrUserNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, mfa_services.ErrTooManyAttempts):
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
