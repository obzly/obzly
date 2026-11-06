package users_controllers

import (
	users_services "obzly/internal/features/users/services"
)

var userController = &UserController{
	users_services.GetUserService(),
	users_services.GetSettingsService(),
}

var settingsController = &SettingsController{
	users_services.GetSettingsService(),
}

var managementController = &ManagementController{
	users_services.GetManagementService(),
}

var userPlanController = &UserPlanController{
	users_services.GetUserPlanService(),
}

func GetUserController() *UserController {
	return userController
}

func GetSettingsController() *SettingsController {
	return settingsController
}

func GetManagementController() *ManagementController {
	return managementController
}

func GetUserPlanController() *UserPlanController {
	return userPlanController
}
