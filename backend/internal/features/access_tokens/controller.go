package access_tokens

import (
	"errors"
	"net/http"

	mfa_services "obzly/internal/features/users/mfa/services"
	users_middleware "obzly/internal/features/users/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AccessTokenController struct {
	accessTokenService *AccessTokenService
}

func (c *AccessTokenController) RegisterRoutes(router *gin.RouterGroup) {
	routes := router.Group("/access-tokens")
	routes.GET("", c.ListAccessTokens)
	routes.POST("", c.CreateAccessToken)
	routes.DELETE("/:tokenId", c.RevokeAccessToken)
}

// ListAccessTokens
// @Summary List the current user's active access tokens
// @Tags access-tokens
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ListAccessTokensResponseDTO
// @Failure 401 {object} map[string]string
// @Router /access-tokens [get]
func (c *AccessTokenController) ListAccessTokens(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	response, err := c.accessTokenService.List(user)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list access tokens"})
		return
	}
	ctx.JSON(http.StatusOK, response)
}

// CreateAccessToken
// @Summary Create a personal access token for the MCP endpoint
// @Description The full token is returned once. Requires the MFA code when MFA is enabled.
// @Tags access-tokens
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body CreateAccessTokenRequestDTO true "Token name, lifetime and MFA code"
// @Success 200 {object} CreateAccessTokenResponseDTO
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 429 {object} map[string]string
// @Router /access-tokens [post]
func (c *AccessTokenController) CreateAccessToken(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var request CreateAccessTokenRequestDTO
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Give the token a name and a lifetime of 30, 90 or 365 days"})
		return
	}

	response, err := c.accessTokenService.Create(user, &request, users_middleware.SessionSatisfiesMFA(ctx))
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, response)
	case errors.Is(err, ErrMFACodeRequired):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "mfaRequired": true})
	case errors.Is(err, mfa_services.ErrTooManyAttempts):
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
	case errors.Is(err, ErrMFACodeInvalid), errors.Is(err, ErrTooManyTokens):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create access token"})
	}
}

// RevokeAccessToken
// @Summary Revoke one of the current user's access tokens
// @Tags access-tokens
// @Produce json
// @Security BearerAuth
// @Param tokenId path string true "Access token ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /access-tokens/{tokenId} [delete]
func (c *AccessTokenController) RevokeAccessToken(ctx *gin.Context) {
	user, ok := users_middleware.GetUserFromContext(ctx)
	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	tokenID, err := uuid.Parse(ctx.Param("tokenId"))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid token ID"})
		return
	}

	if err := c.accessTokenService.Revoke(user, tokenID); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke access token"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "Access token revoked"})
}
