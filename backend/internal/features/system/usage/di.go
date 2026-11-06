package system_usage

var controller = &Controller{&Service{}}

func GetController() *Controller {
	return controller
}
