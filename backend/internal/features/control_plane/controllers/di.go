package control_plane_controllers

import (
	control_plane_services "obzly/internal/features/control_plane/services"
	users_repositories "obzly/internal/features/users/repositories"
)

var userRepository = &users_repositories.UserRepository{}

var siteController = &SiteController{
	siteService:  control_plane_services.GetSiteService(),
	grantService: control_plane_services.GetGrantService(),
}

var grantController = &GrantController{
	grantService:   control_plane_services.GetGrantService(),
	userRepository: userRepository,
}

var signingKeyController = &SigningKeyController{
	signingKeyService: control_plane_services.GetSigningKeyService(),
}

var jwksController = &JWKSController{
	signingKeyService: control_plane_services.GetSigningKeyService(),
}

var proxyController = &ProxyController{
	siteService:      control_plane_services.GetSiteService(),
	grantService:     control_plane_services.GetGrantService(),
	siteTokenService: control_plane_services.GetSiteTokenService(),
}

func GetSiteController() *SiteController   { return siteController }
func GetGrantController() *GrantController { return grantController }

var ssoSiteMappingController = &SSOSiteMappingController{
	service: control_plane_services.GetSSOSiteMappingService(),
}

func GetSSOSiteMappingController() *SSOSiteMappingController { return ssoSiteMappingController }
func GetSigningKeyController() *SigningKeyController         { return signingKeyController }
func GetJWKSController() *JWKSController                     { return jwksController }
func GetProxyController() *ProxyController                   { return proxyController }
