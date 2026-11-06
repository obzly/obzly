package mfa_dto

// SetupResponseDTO carries a new, not-yet-enabled secret: Secret for manual entry
// and OtpAuthURL for the QR code.
type SetupResponseDTO struct {
	Secret     string `json:"secret"`
	OtpAuthURL string `json:"otpAuthUrl"`
}

type EnableRequestDTO struct {
	Code string `json:"code" binding:"required,len=6,numeric"`
}

type DisableRequestDTO struct {
	Code string `json:"code" binding:"required,len=6,numeric"`
}

// StatusResponseDTO reports whether MFA is on and whether the role requires it.
type StatusResponseDTO struct {
	Enabled  bool `json:"enabled"`
	Required bool `json:"required"`
	// SatisfiedByIdentityProvider: the session's second factor was verified by a
	// control plane or trusted SSO provider, so Required doesn't block it.
	SatisfiedByIdentityProvider bool `json:"satisfiedByIdentityProvider"`
}

// LoginSetupRequestDTO, LoginEnableRequestDTO and LoginVerifyRequestDTO carry the
// challenge token from SignIn or an OAuth callback; completing them grants the session.
type LoginSetupRequestDTO struct {
	MFAToken string `json:"mfaToken" binding:"required"`
}

type LoginEnableRequestDTO struct {
	MFAToken string `json:"mfaToken" binding:"required"`
	Code     string `json:"code"     binding:"required,len=6,numeric"`
}

type LoginVerifyRequestDTO struct {
	MFAToken string `json:"mfaToken" binding:"required"`
	Code     string `json:"code"     binding:"required,len=6,numeric"`
}
