package control_plane_repositories

import (
	"errors"

	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"

	"gorm.io/gorm"
)

type SiteRepository struct{}

func (r *SiteRepository) Create(site *control_plane_models.Site) error {
	return storage.GetDb().Create(site).Error
}

func (r *SiteRepository) GetByID(id string) (*control_plane_models.Site, error) {
	var site control_plane_models.Site
	err := storage.GetDb().Where("id = ?", id).First(&site).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &site, nil
}

func (r *SiteRepository) List() ([]*control_plane_models.Site, error) {
	var sites []*control_plane_models.Site
	err := storage.GetDb().Order("name ASC").Find(&sites).Error
	return sites, err
}

func (r *SiteRepository) Update(site *control_plane_models.Site) error {
	return storage.GetDb().Save(site).Error
}

func (r *SiteRepository) Delete(id string) error {
	return storage.GetDb().Where("id = ?", id).Delete(&control_plane_models.Site{}).Error
}
