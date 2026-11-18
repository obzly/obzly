package useraccess

import users_repositories "obzly/internal/features/users/repositories"

var controller = &Controller{&Service{&users_repositories.UserRepository{}, &repository{}}}

func GetController() *Controller {
	return controller
}
