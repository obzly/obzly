package projects_repositories

import (
	"fmt"
	"time"

	projects_models "obzly/internal/features/projects/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ProjectRepository struct{}

func (r *ProjectRepository) CreateProject(project *projects_models.Project) error {
	if project.ID == uuid.Nil {
		project.ID = uuid.New()
	}
	if project.CreatedAt.IsZero() {
		project.CreatedAt = time.Now().UTC()
	}

	return storage.GetDb().Create(project).Error
}

// CreateProjectWithOwnerMembership creates the project and its owner membership in one
// transaction. Access checks key off membership rows, so a project without its owner
// membership could never be managed or deleted through the API.
func (r *ProjectRepository) CreateProjectWithOwnerMembership(
	project *projects_models.Project,
	membership *projects_models.ProjectMembership,
) error {
	if project.ID == uuid.Nil {
		project.ID = uuid.New()
	}
	if project.CreatedAt.IsZero() {
		project.CreatedAt = time.Now().UTC()
	}
	membership.ProjectID = project.ID
	if membership.ID == uuid.Nil {
		membership.ID = uuid.New()
	}
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = time.Now().UTC()
	}

	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(project).Error; err != nil {
			return fmt.Errorf("failed to create project: %w", err)
		}
		if err := tx.Create(membership).Error; err != nil {
			return fmt.Errorf("failed to create project membership: %w", err)
		}
		return nil
	})
}

func (r *ProjectRepository) GetProjectByID(projectID uuid.UUID) (*projects_models.Project, error) {
	var project projects_models.Project

	if err := storage.GetDb().Preload("Plan").Where("id = ?", projectID).First(&project).Error; err != nil {
		return nil, err
	}

	return &project, nil
}

// GetSystemProject returns the reserved system project, or nil if it has
// not been created yet.
func (r *ProjectRepository) GetSystemProject() (*projects_models.Project, error) {
	var projects []projects_models.Project
	if err := storage.GetDb().Where("is_system").Limit(1).Find(&projects).Error; err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return nil, nil
	}
	return &projects[0], nil
}

func (r *ProjectRepository) UpdateProject(project *projects_models.Project) error {
	return storage.GetDb().Save(project).Error
}

func (r *ProjectRepository) DeleteProject(projectID uuid.UUID) error {
	return storage.GetDb().Delete(&projects_models.Project{}, projectID).Error
}

func (r *ProjectRepository) GetAllProjects() ([]*projects_models.Project, error) {
	var projects []*projects_models.Project

	err := storage.GetDb().Preload("Plan").Order("created_at DESC").Find(&projects).Error

	return projects, err
}

// CountUserProjects counts projects other than the system one.
func (r *ProjectRepository) CountUserProjects() (int64, error) {
	var count int64
	err := storage.GetDb().Model(&projects_models.Project{}).Where("NOT is_system").Count(&count).Error
	return count, err
}

func (r *ProjectRepository) GetProjectsCountByOwnerIDAndPlanID(ownerID uuid.UUID, planID uuid.UUID) (int64, error) {
	var count int64

	err := storage.GetDb().
		Table("projects").
		Joins("INNER JOIN project_memberships ON projects.id = project_memberships.project_id").
		Where("project_memberships.user_id = ? AND project_memberships.role = 'OWNER' AND projects.plan_id = ?", ownerID, planID).
		Count(&count).
		Error

	return count, err
}
