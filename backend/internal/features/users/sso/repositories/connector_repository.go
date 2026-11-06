package sso_repositories

import (
	"errors"

	sso_models "obzly/internal/features/users/sso/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ConnectorRepository struct{}

func (r *ConnectorRepository) Create(connector *sso_models.Connector) error {
	return storage.GetDb().Create(connector).Error
}

func (r *ConnectorRepository) Update(connector *sso_models.Connector) error {
	return storage.GetDb().Save(connector).Error
}

func (r *ConnectorRepository) Delete(id uuid.UUID) error {
	return storage.GetDb().Delete(&sso_models.Connector{}, "id = ?", id).Error
}

func (r *ConnectorRepository) GetByID(id uuid.UUID) (*sso_models.Connector, error) {
	var connector sso_models.Connector
	err := storage.GetDb().Where("id = ?", id).First(&connector).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &connector, nil
}

func (r *ConnectorRepository) ListAll() ([]*sso_models.Connector, error) {
	var connectors []*sso_models.Connector
	err := storage.GetDb().Order("created_at ASC").Find(&connectors).Error
	return connectors, err
}

func (r *ConnectorRepository) ListEnabled() ([]*sso_models.Connector, error) {
	var connectors []*sso_models.Connector
	err := storage.GetDb().Where("enabled = ?", true).Order("created_at ASC").Find(&connectors).Error
	return connectors, err
}
