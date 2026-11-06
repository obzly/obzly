package projects_controllers

import (
	projects_services "obzly/internal/features/projects/services"
)

var projectController = &ProjectController{
	projects_services.GetProjectService(),
	projects_services.GetRetentionPolicyService(),
	projects_services.GetPromotedFieldService(),
}

var membershipController = &MembershipController{
	projects_services.GetMembershipService(),
}

func GetProjectController() *ProjectController {
	return projectController
}

func GetMembershipController() *MembershipController {
	return membershipController
}
