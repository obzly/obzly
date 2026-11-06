package users_enums

type UserPlanType string

const (
	// received by default for new users
	UserPlanTypeDefault UserPlanType = "DEFAULT"
	UserPlanTypePro     UserPlanType = "EXTENDED"
)
