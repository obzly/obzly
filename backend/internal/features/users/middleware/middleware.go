package users_middleware

import (
	"errors"
	"net/http"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_services "obzly/internal/features/users/services"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates the bearer token and puts the user in the context.
func AuthMiddleware(userService *users_services.UserService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		token := ctx.GetHeader("Authorization")
		if token == "" {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
			ctx.Abort()
			return
		}

		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}

		user, claims, err := userService.GetUserAndClaimsFromToken(token)
		if errors.Is(err, users_services.ErrAuthUnavailable) {
			ctx.Header("Retry-After", "5")
			ctx.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Sign-in check is temporarily unavailable, try again shortly",
			})
			ctx.Abort()
			return
		}
		if err != nil {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			ctx.Abort()
			return
		}

		ctx.Set("user", user)
		userService.RecordActivity(user.ID)
		// Set only from a fully validated token, so RequireMFAIfEnforced can trust it.
		if origin, _ := claims[users_services.AuthOriginClaim].(string); origin != "" {
			ctx.Set(AuthOriginContextKey, origin)
		}
		ctx.Set(mfaSatisfiedContextKey, users_services.ClaimsSatisfyMFA(claims))
		ctx.Next()
	}
}

// AuthOriginContextKey holds the validated token's AuthOriginClaim, if any.
const AuthOriginContextKey = "auth_origin"

const mfaSatisfiedContextKey = "mfa_satisfied_elsewhere"

// IsControlPlaneSession reports whether the session came from a control-plane
// token exchange.
func IsControlPlaneSession(ctx *gin.Context) bool {
	return ctx.GetString(AuthOriginContextKey) == users_services.AuthOriginControlPlane
}

// SessionSatisfiesMFA reports whether the session's second factor was verified
// by the control plane or a trusted SSO provider.
func SessionSatisfiesMFA(ctx *gin.Context) bool {
	return ctx.GetBool(mfaSatisfiedContextKey)
}

// RequireMFAIfEnforced blocks an Admin without MFA set up on every request, not
// just at login. Mount the MFA self-service routes before adding it (see cmd/main.go).
func RequireMFAIfEnforced() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		user, ok := GetUserFromContext(ctx)
		if !ok {
			ctx.Next()
			return
		}

		// MFA already verified centrally or by a trusted SSO provider. Claim-gated: a
		// local or password sign-in to the same account is still enforced.
		if SessionSatisfiesMFA(ctx) {
			ctx.Next()
			return
		}

		if user.IsMFARequired() && !user.MFAEnabled {
			ctx.JSON(http.StatusForbidden, gin.H{
				"error":            "MFA setup required",
				"mfaSetupRequired": true,
			})
			ctx.Abort()
			return
		}

		ctx.Next()
	}
}

func RequireRole(requiredRole users_enums.UserRole) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		userInterface, exists := ctx.Get("user")
		if !exists {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
			ctx.Abort()
			return
		}

		user, ok := userInterface.(*users_models.User)
		if !ok {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user context"})
			ctx.Abort()
			return
		}

		if user.Role != requiredRole {
			ctx.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
			ctx.Abort()
			return
		}

		ctx.Next()
	}
}

// GetUserFromContext returns the user AuthMiddleware stored.
func GetUserFromContext(ctx *gin.Context) (*users_models.User, bool) {
	userInterface, exists := ctx.Get("user")
	if !exists {
		return nil, false
	}

	user, ok := userInterface.(*users_models.User)

	return user, ok
}
