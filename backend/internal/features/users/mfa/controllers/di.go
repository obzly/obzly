package mfa_controllers

import (
	mfa_services "obzly/internal/features/users/mfa/services"
)

var mfaController = &MFAController{mfa_services.GetMFAService()}

func GetMFAController() *MFAController {
	return mfaController
}
