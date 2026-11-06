package control_plane_controllers

import (
	"net/http"

	control_plane_services "obzly/internal/features/control_plane/services"

	"github.com/gin-gonic/gin"
)

// JWKSController serves the control plane's public signing keys, which Sites fetch
// to verify token-exchange assertions. Public by design, like any OIDC JWKS.
type JWKSController struct {
	signingKeyService *control_plane_services.SigningKeyService
}

func (c *JWKSController) RegisterRoutes(r *gin.Engine) {
	r.GET("/.well-known/jwks.json", c.GetJWKS)
	r.GET("/.well-known/openid-configuration", c.GetOpenIDConfiguration)
}

// GetOpenIDConfiguration
// @Summary Control-plane OIDC discovery
// @Description For AWS and Azure to trust the control plane as the issuer of federated cloud connection tokens
// @Tags control-plane
// @Produce json
// @Success 200
// @Router /.well-known/openid-configuration [get]
func (c *JWKSController) GetOpenIDConfiguration(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, control_plane_services.OpenIDConfiguration())
}

// GetJWKS
// @Summary Control-plane JWKS
// @Description Public signing keys for verifying token-exchange assertions
// @Tags control-plane
// @Produce json
// @Success 200
// @Router /.well-known/jwks.json [get]
func (c *JWKSController) GetJWKS(ctx *gin.Context) {
	jwks, err := c.signingKeyService.JWKS()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load signing keys"})
		return
	}
	ctx.Header("Cache-Control", "public, max-age=30")
	ctx.JSON(http.StatusOK, jwks)
}
