package users_services

import (
	users_models "obzly/internal/features/users/models"
	user_repositories "obzly/internal/features/users/repositories"
	sso_services "obzly/internal/features/users/sso/services"
)

var secretKeyRepository = &user_repositories.SecretKeyRepository{}
var userRepository = &user_repositories.UserRepository{}
var usersSettingsRepository = &user_repositories.UsersSettingsRepository{}
var userPlanRepository = &user_repositories.UserPlanRepository{}
var centralIdentityRepository = &user_repositories.CentralIdentityRepository{}

var userService = &UserService{
	userRepository,
	secretKeyRepository,
	userPlanRepository,
	settingsService,
	nil,
	sso_services.GetIdentityRepository(),
	nil,
	centralIdentityRepository,
	nil, // ssoSiteGrantSyncer - set only on a control plane
}
var settingsService = &SettingsService{
	usersSettingsRepository,
	nil,
}
var managementService = &UserManagementService{
	userRepository,
	userPlanService,
	nil,
}
var userPlanService = &UserPlanService{
	userPlanRepository,
	nil,
	userService,
}

func init() {
	users_models.SetMFAForEveryone(settingsService.MFARequiredForEveryone)
}

func GetUserService() *UserService {
	return userService
}

func GetSettingsService() *SettingsService {
	return settingsService
}

func GetManagementService() *UserManagementService {
	return managementService
}

func GetUserPlanService() *UserPlanService {
	return userPlanService
}
