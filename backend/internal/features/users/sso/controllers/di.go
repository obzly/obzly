package sso_controllers

import (
	sso_services "obzly/internal/features/users/sso/services"
)

var connectorController = &ConnectorController{
	sso_services.GetConnectorService(),
}

func GetConnectorController() *ConnectorController {
	return connectorController
}
