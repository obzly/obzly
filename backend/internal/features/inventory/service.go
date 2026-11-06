package inventory

import (
	"errors"
	"strings"
	"time"

	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultAssetLimit = 2000
	maxAssetLimit     = 5000
	maxChildren       = 200
	maxParents        = 6
)

var (
	ErrNotFound  = errors.New("asset not found")
	ErrForbidden = errors.New("you can't read this project")
)

// Service answers inventory reads, limited to the projects the user can
// read.
type Service struct {
	repo *repository
	now  func() time.Time
}

type assetFilter struct {
	kinds     []string
	health    []string
	states    []string
	cluster   string
	namespace string
	search    string
}

// Summary counts the user's assets by kind, health and state.
func (s *Service) Summary(user *users_models.User, projectID string) (*SummaryDTO, error) {
	projects, ids, err := s.projectsFor(user, projectID)
	if err != nil {
		return nil, err
	}
	summary := &SummaryDTO{
		Kinds: map[string]map[string]int64{}, States: map[string]int64{}, Health: map[string]int64{},
		Projects: []ProjectCountDTO{},
	}
	if len(ids) == 0 {
		return summary, nil
	}
	rows, err := s.repo.summary(ids)
	if err != nil {
		return nil, err
	}
	perProject := map[uuid.UUID]int64{}
	for _, row := range rows {
		if summary.Kinds[row.Kind] == nil {
			summary.Kinds[row.Kind] = map[string]int64{}
		}
		summary.Kinds[row.Kind][row.Health] += row.Count
		summary.States[row.State] += row.Count
		summary.Health[row.Health] += row.Count
		summary.Total += row.Count
		perProject[row.ProjectID] += row.Count
	}
	for _, id := range ids {
		if perProject[id] > 0 {
			summary.Projects = append(
				summary.Projects,
				ProjectCountDTO{ID: id, Name: projects[id], Assets: perProject[id]},
			)
		}
	}
	return summary, nil
}

// ListAssets is the user's assets matching filter, worst health first, at
// most limit of them (Total says how many matched).
func (s *Service) ListAssets(
	user *users_models.User,
	projectID string,
	filter assetFilter,
	limit int,
) (*AssetListDTO, error) {
	projects, ids, err := s.projectsFor(user, projectID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultAssetLimit
	}
	limit = min(limit, maxAssetLimit)
	result := &AssetListDTO{Assets: []AssetDTO{}}
	if len(ids) == 0 {
		return result, nil
	}
	assets, total, err := s.repo.listAssets(ids, filter, limit)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		result.Assets = append(result.Assets, AssetDTO{Asset: a, ProjectName: projects[a.ProjectID]})
	}
	result.Total = total
	return result, nil
}

// GetAsset is one asset with its parents, children and recent changes.
func (s *Service) GetAsset(user *users_models.User, id uuid.UUID) (*AssetDetailDTO, error) {
	asset, err := s.repo.asset(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	projects, _, err := s.projectsFor(user, asset.ProjectID.String())
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	detail := &AssetDetailDTO{
		Asset:    AssetDTO{Asset: *asset, ProjectName: projects[asset.ProjectID]},
		Parents:  []AssetRefDTO{},
		Children: []AssetDTO{},
	}
	for key, steps := asset.ParentKey, 0; key != "" && steps < maxParents; steps++ {
		parent, err := s.repo.assetByKey(asset.ProjectID, key)
		if err != nil {
			break
		}
		detail.Parents = append([]AssetRefDTO{refOf(parent)}, detail.Parents...)
		key = parent.ParentKey
	}
	if asset.NodeKey != "" {
		if node, err := s.repo.assetByKey(asset.ProjectID, asset.NodeKey); err == nil {
			ref := refOf(node)
			detail.Node = &ref
		}
	}
	children, err := s.repo.children(asset.ProjectID, asset.Key, maxChildren)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		detail.Children = append(detail.Children, AssetDTO{Asset: child, ProjectName: projects[child.ProjectID]})
	}
	detail.Changes, err = s.repo.changes([]uuid.UUID{asset.ProjectID}, asset.Key, s.now().Add(-keepChanges), 50)
	return detail, err
}

// ListChanges is the change feed of the user's projects since hours ago.
func (s *Service) ListChanges(user *users_models.User, projectID string, hours, limit int) ([]ChangeDTO, error) {
	projects, ids, err := s.projectsFor(user, projectID)
	if err != nil {
		return nil, err
	}
	result := []ChangeDTO{}
	if len(ids) == 0 {
		return result, nil
	}
	if hours <= 0 || hours > 24*7 {
		hours = 24
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	changes, err := s.repo.changes(ids, "", s.now().Add(-time.Duration(hours)*time.Hour), limit)
	if err != nil {
		return nil, err
	}
	for _, change := range changes {
		result = append(result, ChangeDTO{Change: change, ProjectName: projects[change.ProjectID]})
	}
	return result, nil
}

// projectsFor is the projects the user can read (names by id), narrowed to
// projectID when one is given.
func (s *Service) projectsFor(user *users_models.User, projectID string) (map[uuid.UUID]string, []uuid.UUID, error) {
	list, err := projects_services.GetProjectService().GetUserProjects(user)
	if err != nil {
		return nil, nil, err
	}
	names := map[uuid.UUID]string{}
	var ids []uuid.UUID
	for _, p := range list.Projects {
		if projectID != "" && p.ID.String() != strings.TrimSpace(projectID) {
			continue
		}
		names[p.ID] = p.Name
		ids = append(ids, p.ID)
	}
	if projectID != "" && len(ids) == 0 {
		return nil, nil, ErrForbidden
	}
	return names, ids, nil
}

func refOf(a *Asset) AssetRefDTO {
	return AssetRefDTO{ID: a.ID, Kind: a.Kind, Subtype: a.Subtype, Name: a.Name, Health: a.Health}
}
