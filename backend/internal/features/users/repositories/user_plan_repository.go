package users_repositories

import (
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserPlanRepository struct{}

func (r *UserPlanRepository) GetPlans() ([]*users_models.UserPlan, error) {
	var plans []*users_models.UserPlan
	if err := storage.GetDb().Order("name ASC").Find(&plans).Error; err != nil {
		return nil, err
	}
	return plans, nil
}

func (r *UserPlanRepository) GetPlanByID(id uuid.UUID) (*users_models.UserPlan, error) {
	var plan users_models.UserPlan
	if err := storage.GetDb().Where("id = ?", id).First(&plan).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &plan, nil
}

func (r *UserPlanRepository) GetPlanByType(planType users_enums.UserPlanType) (*users_models.UserPlan, error) {
	var plan users_models.UserPlan

	if err := storage.GetDb().Where("type = ?", planType).First(&plan).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}

		return nil, err
	}

	return &plan, nil
}

func (r *UserPlanRepository) CreatePlan(plan *users_models.UserPlan) error {
	return storage.GetDb().Create(plan).Error
}

func (r *UserPlanRepository) UpdatePlan(plan *users_models.UserPlan) error {
	return storage.GetDb().Save(plan).Error
}

func (r *UserPlanRepository) DeletePlan(id uuid.UUID) error {
	return storage.GetDb().Where("id = ?", id).Delete(&users_models.UserPlan{}).Error
}
