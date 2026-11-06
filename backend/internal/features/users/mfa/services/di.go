package mfa_services

import (
	users_repositories "obzly/internal/features/users/repositories"
)

var (
	userRepository      = &users_repositories.UserRepository{}
	secretKeyRepository = &users_repositories.SecretKeyRepository{}
	mfaService          = &Service{userRepository, secretKeyRepository, nil}
)

func GetMFAService() *Service {
	return mfaService
}
