package users_services

import (
	"errors"
	"fmt"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
)

type UserPlanService struct {
	userPlanRepository   *users_repositories.UserPlanRepository
	auditLogWriter       users_interfaces.AuditLogWriter
	planDeletionListener users_interfaces.PlanDeletionListener
}

func (s *UserPlanService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

// GetPlans returns every plan to an Admin and only public plans to anyone else:
// non-public plans can hold internal pricing or custom deals.
func (s *UserPlanService) GetPlans(requester *users_models.User) ([]*users_models.UserPlan, error) {
	plans, err := s.userPlanRepository.GetPlans()
	if err != nil {
		return nil, err
	}

	if requester != nil && requester.Role == users_enums.UserRoleAdmin {
		return plans, nil
	}

	publicPlans := make([]*users_models.UserPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.IsPublic {
			publicPlans = append(publicPlans, plan)
		}
	}
	return publicPlans, nil
}

func (s *UserPlanService) CreatePlan(
	request *users_dto.CreatePlanRequestDTO,
	creator *users_models.User,
) (*users_models.UserPlan, error) {
	if !creator.CanUpdateSettings() {
		return nil, errors.New("insufficient permissions to create plans")
	}

	if request.Type == users_enums.UserPlanTypeDefault {
		existingBasicPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing DEFAULT plan: %w", err)
		}
		if existingBasicPlan != nil {
			return nil, errors.New("DEFAULT plan already exists, only one DEFAULT plan is allowed")
		}
	}

	plan := &users_models.UserPlan{
		ID:                   uuid.New(),
		Name:                 request.Name,
		Type:                 request.Type,
		IsPublic:             request.IsPublic,
		WarningText:          request.WarningText,
		UpgradeText:          request.UpgradeText,
		LogsPerSecondLimit:   request.LogsPerSecondLimit,
		MaxLogsAmount:        request.MaxLogsAmount,
		MaxLogsSizeMB:        request.MaxLogsSizeMB,
		MaxLogsLifeDays:      request.MaxLogsLifeDays,
		MaxLogSizeKB:         request.MaxLogSizeKB,
		AllowedProjectsCount: request.AllowedProjectsCount,
	}

	if err := s.userPlanRepository.CreatePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to create plan: %w", err)
	}

	s.auditLogWriter.WriteAuditLog(
		fmt.Sprintf("Plan created: %s (type: %s)", plan.Name, plan.Type),
		&creator.ID,
		nil,
	)

	return plan, nil
}

func (s *UserPlanService) UpdatePlan(
	id uuid.UUID,
	request *users_dto.UpdatePlanRequestDTO,
	updater *users_models.User,
) (*users_models.UserPlan, error) {
	if !updater.CanUpdateSettings() {
		return nil, errors.New("insufficient permissions to update plans")
	}

	plan, err := s.userPlanRepository.GetPlanByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan: %w", err)
	}
	if plan == nil {
		return nil, errors.New("plan not found")
	}

	if request.Type != nil && *request.Type == users_enums.UserPlanTypeDefault {
		existingDefaultPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing DEFAULT plan: %w", err)
		}
		if existingDefaultPlan != nil && existingDefaultPlan.ID != plan.ID {
			return nil, errors.New("DEFAULT plan already exists, only one DEFAULT plan is allowed")
		}
	}

	if request.Name != nil {
		plan.Name = *request.Name
	}
	if request.Type != nil {
		plan.Type = *request.Type
	}
	if request.IsPublic != nil {
		plan.IsPublic = *request.IsPublic
	}
	if request.WarningText != nil {
		plan.WarningText = *request.WarningText
	}
	if request.UpgradeText != nil {
		plan.UpgradeText = *request.UpgradeText
	}
	if request.LogsPerSecondLimit != nil {
		plan.LogsPerSecondLimit = *request.LogsPerSecondLimit
	}
	if request.MaxLogsAmount != nil {
		plan.MaxLogsAmount = *request.MaxLogsAmount
	}
	if request.MaxLogsSizeMB != nil {
		plan.MaxLogsSizeMB = *request.MaxLogsSizeMB
	}
	if request.MaxLogsLifeDays != nil {
		plan.MaxLogsLifeDays = *request.MaxLogsLifeDays
	}
	if request.MaxLogSizeKB != nil {
		plan.MaxLogSizeKB = *request.MaxLogSizeKB
	}
	if request.AllowedProjectsCount != nil {
		plan.AllowedProjectsCount = *request.AllowedProjectsCount
	}

	if err := s.userPlanRepository.UpdatePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to update plan: %w", err)
	}

	s.auditLogWriter.WriteAuditLog(
		fmt.Sprintf("Plan updated: %s", plan.Name),
		&updater.ID,
		nil,
	)

	return plan, nil
}

func (s *UserPlanService) DeletePlan(
	id uuid.UUID,
	deleter *users_models.User,
) error {
	if !deleter.CanUpdateSettings() {
		return errors.New("insufficient permissions to delete plans")
	}

	plan, err := s.userPlanRepository.GetPlanByID(id)
	if err != nil {
		return fmt.Errorf("failed to get plan: %w", err)
	}
	if plan == nil {
		return errors.New("plan not found")
	}

	if err := s.planDeletionListener.OnBeforePlanDeletion(id); err != nil {
		return err
	}

	if err := s.userPlanRepository.DeletePlan(id); err != nil {
		return fmt.Errorf("failed to delete plan: %w", err)
	}

	s.auditLogWriter.WriteAuditLog(
		fmt.Sprintf("Plan deleted: %s", plan.Name),
		&deleter.ID,
		nil,
	)

	return nil
}

func (s *UserPlanService) GetDefaultPlan() (*users_models.UserPlan, error) {
	return s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
}
