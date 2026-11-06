package sso_services

import (
	user_repositories "obzly/internal/features/users/repositories"
	sso_repositories "obzly/internal/features/users/sso/repositories"
)

var connectorRepository = &sso_repositories.ConnectorRepository{}
var groupMappingRepository = &sso_repositories.GroupMappingRepository{}
var identityRepository = &sso_repositories.IdentityRepository{}
var secretKeyRepository = &user_repositories.SecretKeyRepository{}

var connectorService = &ConnectorService{
	connectorRepository,
	secretKeyRepository,
	nil,
	groupMappingRepository,
}

func GetConnectorService() *ConnectorService {
	return connectorService
}

// GetIdentityRepository is used by UserService for SSO account linking.
func GetIdentityRepository() *sso_repositories.IdentityRepository {
	return identityRepository
}
