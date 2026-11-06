package control_plane_services

import (
	"fmt"

	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
)

var (
	siteRepository       = &control_plane_repositories.SiteRepository{}
	grantRepository      = &control_plane_repositories.GrantRepository{}
	signingKeyRepository = &control_plane_repositories.SigningKeyRepository{}
	userRepository       = &users_repositories.UserRepository{}
	secretKeyRepository  = &users_repositories.SecretKeyRepository{}
)

var siteService = &SiteService{siteRepository: siteRepository}

var grantService = &GrantService{
	grantRepository: grantRepository,
	userRepository:  userRepository,
}

var ssoSiteMappingService = &SSOSiteMappingService{
	repository:      &control_plane_repositories.SSOSiteMappingRepository{},
	grantRepository: grantRepository,
}

func GetSSOSiteMappingService() *SSOSiteMappingService {
	return ssoSiteMappingService
}

var signingKeyService = &SigningKeyService{
	repository:          signingKeyRepository,
	secretKeyRepository: secretKeyRepository,
}

var assertionService = &AssertionService{
	signingKeyService: signingKeyService,
	grantService:      grantService,
}

var siteTokenService = &SiteTokenService{
	assertionService:    assertionService,
	secretKeyRepository: secretKeyRepository,
}

func GetSiteService() *SiteService             { return siteService }
func GetGrantService() *GrantService           { return grantService }
func GetSigningKeyService() *SigningKeyService { return signingKeyService }
func GetAssertionService() *AssertionService   { return assertionService }
func GetSiteTokenService() *SiteTokenService   { return siteTokenService }

// SetupDependencies wires audit logging (an interface parameter to avoid an import
// cycle) and the session-revocation listener. It must not touch the database: it runs
// before migrations.
func SetupDependencies(auditLogWriter users_interfaces.AuditLogWriter) {
	siteService.SetAuditLogWriter(auditLogWriter)
	grantService.SetAuditLogWriter(auditLogWriter)
	signingKeyService.SetAuditLogWriter(auditLogWriter)
	ssoSiteMappingService.SetAuditLogWriter(auditLogWriter)
	users_services.SetSessionRevocationListener(NewRevocationListener())
	users_services.GetUserService().SetSSOSiteGrantSyncer(ssoSiteMappingService)
}

// EnsureSigningKey makes sure an active assertion-signing key exists. Must run after
// migrations: on a fresh database the table doesn't exist before then.
func EnsureSigningKey() error {
	if err := signingKeyService.EnsureActiveKey(); err != nil {
		return fmt.Errorf("failed to ensure an active control-plane signing key: %w", err)
	}
	if err := EnsureFederationKey(); err != nil {
		return fmt.Errorf("failed to ensure the cloud federation key: %w", err)
	}
	return nil
}
