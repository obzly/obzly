package system_siteinfo

import (
	"net/http"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"

	"github.com/gin-gonic/gin"
)

type Controller struct{}

func (c *Controller) RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/site/info", c.GetSiteInfo)
}

// GetSiteInfo
// @Summary Describe this backend for a control plane
// @Description Unauthenticated, secret-free: which Site this backend is (SITE_ID/SITE_NAME), its build version, and whether it's a standalone install, a Site enrolled in a control plane, or a control plane itself. Used by a control-plane admin's "check connection" step when registering a Site.
// @Tags system/site-info
// @Produce json
// @Success 200 {object} SiteInfoResponse
// @Router /site/info [get]
func (c *Controller) GetSiteInfo(ctx *gin.Context) {
	env := config.GetEnv()
	info := describe(env)
	if env.SiteExchangeEnabled() {
		if key, err := sharedconfig.PublicKey(); err == nil {
			info.SharedConfigKey = key
		}
	}
	ctx.JSON(http.StatusOK, info)
}

// describe maps config to the wire shape.
func describe(env config.EnvVariables) SiteInfoResponse {
	mode := "standalone"
	switch {
	case env.ServiceMode.IsControlPlane():
		mode = "control-plane"
	case env.SiteExchangeEnabled():
		mode = "site"
	}

	return SiteInfoResponse{
		SiteID:                 env.SiteID,
		SiteName:               env.SiteName,
		Version:                env.AppVersion,
		Mode:                   mode,
		ControlPlaneConfigured: env.SiteExchangeEnabled(),
	}
}
