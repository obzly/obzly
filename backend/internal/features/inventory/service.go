package inventory

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
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
	historyDays       = 7
	// previousAge: change pills compare with the counts this old; previousSlack: how much
	// older they may be when that hour has none (a build was missed).
	previousAge   = 24 * time.Hour
	previousSlack = 2 * time.Hour
)

// Sorts of the asset list, the Assets table's columns plus attention (Needs attention: worst health,
// leaves such as pods before the namespaces repeating their reason).
const (
	SortHealth    = "health"
	SortKind      = "kind"
	SortName      = "name"
	SortCPU       = "cpu"
	SortMemory    = "memory"
	SortRestarts  = "restarts"
	SortAge       = "age"
	SortLastSeen  = "lastSeen"
	SortAttention = "attention"
)

const (
	healthOrderSQL = "CASE health WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 WHEN 'healthy' THEN 2 " +
		"WHEN 'unknown' THEN 3 ELSE 4 END"
	// The kind filters' hierarchy order in the UI.
	kindOrderSQL = "CASE kind WHEN 'cluster' THEN 0 WHEN 'node' THEN 1 WHEN 'host' THEN 2 WHEN 'namespace' THEN 3 " +
		"WHEN 'workload' THEN 4 WHEN 'pod' THEN 5 WHEN 'container' THEN 6 WHEN 'docker_container' THEN 7 " +
		"WHEN 'service' THEN 8 WHEN 'load_balancer' THEN 9 WHEN 'database' THEN 10 WHEN 'bucket' THEN 11 " +
		"WHEN 'function' THEN 12 WHEN 'scaling_group' THEN 13 ELSE 99 END"
	leafOrderSQL = "CASE kind WHEN 'pod' THEN 0 WHEN 'container' THEN 1 WHEN 'node' THEN 2 WHEN 'host' THEN 3 " +
		"WHEN 'docker_container' THEN 4 WHEN 'workload' THEN 5 WHEN 'service' THEN 6 ELSE 9 END"
)

var (
	ErrNotFound   = errors.New("asset not found")
	ErrForbidden  = errors.New("you can't read this project")
	ErrBadRequest = errors.New("invalid request")
)

// sortKeys are each sort's keys before name and id. A missing metric sorts as -1, below any value.
var sortKeys = map[string][]sortKey{
	SortHealth:    {{healthOrderSQL, true}},
	SortKind:      {{kindOrderSQL, true}},
	SortName:      {},
	SortCPU:       {{"COALESCE((metrics->>'cpuPercent')::float8, -1)", true}},
	SortMemory:    {{"COALESCE((metrics->>'memoryPercent')::float8, -1)", true}},
	SortRestarts:  {{"COALESCE((metrics->>'restarts24h')::float8, -1)", true}},
	SortAge:       {{"first_seen", false}},
	SortLastSeen:  {{"last_seen", false}},
	SortAttention: {{healthOrderSQL, true}, {leafOrderSQL, true}},
}

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
	// parent: only the assets right under this key (with projectId, as keys are per project).
	parent string
	// roots: only the assets without a parent in the inventory.
	roots bool
	// searchProjects are the projects whose name matches search (the service fills it).
	searchProjects []uuid.UUID
}

type assetPage struct {
	sort       string
	descending bool
	offset     int
	limit      int
	// childCounts adds each asset's ChildCount; ancestors adds the list's Ancestors.
	childCounts bool
	ancestors   bool
}

type sortKey struct {
	expr      string
	ascending bool
}

// Summary counts the user's assets by kind, health and state, with the last days' hourly counts
// when withHistory.
func (s *Service) Summary(user *users_models.User, projectID string, withHistory bool) (*SummaryDTO, error) {
	projects, ids, err := s.projectsFor(user, projectID)
	if err != nil {
		return nil, err
	}
	summary := &SummaryDTO{
		Kinds: map[string]map[string]int64{}, States: map[string]int64{}, Health: map[string]int64{},
		Projects: []ProjectCountDTO{}, Live: map[string]int64{}, Regions: []RegionCountDTO{}, Clusters: []string{},
	}
	if withHistory {
		summary.History = &HistoryDTO{Hours: []time.Time{}, Kinds: map[string][]int64{}}
	}
	if len(ids) == 0 {
		return summary, nil
	}
	if withHistory {
		if summary.History, err = s.history(ids); err != nil {
			return nil, err
		}
	}
	rows, err := s.repo.summary(ids)
	if err != nil {
		return nil, err
	}
	perProject := map[uuid.UUID]int64{}
	liveByProject := map[uuid.UUID]map[string]int64{}
	for _, row := range rows {
		if summary.Kinds[row.Kind] == nil {
			summary.Kinds[row.Kind] = map[string]int64{}
		}
		summary.Kinds[row.Kind][row.Health] += row.Count
		summary.States[row.State] += row.Count
		summary.Health[row.Health] += row.Count
		summary.Total += row.Count
		perProject[row.ProjectID] += row.Count
		if liveByProject[row.ProjectID] == nil {
			liveByProject[row.ProjectID] = map[string]int64{}
		}
		if row.State != StateTerminated {
			summary.Live[row.Health] += row.Count
			liveByProject[row.ProjectID][row.Health] += row.Count
		}
	}
	for _, id := range ids {
		if perProject[id] > 0 {
			summary.Projects = append(summary.Projects,
				ProjectCountDTO{ID: id, Name: projects[id], Assets: perProject[id], Live: liveByProject[id]})
		}
	}
	if summary.Regions, err = s.regions(ids, projects); err != nil {
		return nil, err
	}
	if summary.Clusters, err = s.repo.clusters(ids); err != nil {
		return nil, err
	}
	return summary, nil
}

// ListAssets is the page of the user's assets matching filter, in page's order (worst health first
// by default); Total says how many matched.
func (s *Service) ListAssets(
	user *users_models.User,
	projectID string,
	filter assetFilter,
	page assetPage,
) (*AssetListDTO, error) {
	if page.sort == "" {
		page.sort = SortHealth
	}
	if _, ok := sortKeys[page.sort]; !ok {
		return nil, fmt.Errorf("%w: unknown sort %q", ErrBadRequest, page.sort)
	}
	projects, ids, err := s.projectsFor(user, projectID)
	if err != nil {
		return nil, err
	}
	if page.limit <= 0 {
		page.limit = defaultAssetLimit
	}
	page.limit = min(page.limit, maxAssetLimit)
	page.offset = max(page.offset, 0)
	result := &AssetListDTO{Assets: []AssetDTO{}}
	if len(ids) == 0 {
		return result, nil
	}
	if filter.search != "" {
		needle := strings.ToLower(filter.search)
		for _, id := range ids {
			if strings.Contains(strings.ToLower(projects[id]), needle) {
				filter.searchProjects = append(filter.searchProjects, id)
			}
		}
	}
	assets, total, err := s.repo.listAssets(ids, filter, page)
	if err != nil {
		return nil, err
	}
	result.Total = total
	for _, a := range assets {
		result.Assets = append(result.Assets, AssetDTO{Asset: a, ProjectName: projects[a.ProjectID]})
	}
	if page.childCounts && len(assets) > 0 {
		keys := make([]string, len(assets))
		for i, a := range assets {
			keys[i] = a.Key
		}
		counts, err := s.repo.childCounts(ids, keys)
		if err != nil {
			return nil, err
		}
		for i := range result.Assets {
			count := counts[projectKey{result.Assets[i].ProjectID, result.Assets[i].Key}]
			result.Assets[i].ChildCount = &count
		}
	}
	if page.ancestors {
		ancestors, err := s.ancestors(ids, assets)
		if err != nil {
			return nil, err
		}
		for _, a := range ancestors {
			result.Ancestors = append(result.Ancestors, AssetDTO{Asset: a, ProjectName: projects[a.ProjectID]})
		}
	}
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
	keys := make([]string, 0, len(changes))
	for _, change := range changes {
		keys = append(keys, change.AssetKey)
	}
	assetIDs, err := s.repo.idsByKey(ids, keys)
	if err != nil {
		return nil, err
	}
	for _, change := range changes {
		dto := ChangeDTO{Change: change, ProjectName: projects[change.ProjectID]}
		if id, ok := assetIDs[projectKey{change.ProjectID, change.AssetKey}]; ok {
			dto.AssetID = &id
		}
		result = append(result, dto)
	}
	return result, nil
}

// ancestors are the parents, up to the top, of assets that aren't among them.
func (s *Service) ancestors(projectIDs []uuid.UUID, assets []Asset) ([]Asset, error) {
	have := map[projectKey]bool{}
	for _, a := range assets {
		have[projectKey{a.ProjectID, a.Key}] = true
	}
	var result []Asset
	level := assets
	for steps := 0; steps < maxParents && len(level) > 0; steps++ {
		wanted := map[projectKey]bool{}
		var keys []string
		for _, a := range level {
			parent := projectKey{a.ProjectID, a.ParentKey}
			if a.ParentKey == "" || have[parent] || wanted[parent] {
				continue
			}
			wanted[parent] = true
			keys = append(keys, a.ParentKey)
		}
		if len(keys) == 0 {
			break
		}
		found, err := s.repo.assetsByKey(projectIDs, keys)
		if err != nil {
			return nil, err
		}
		level = level[:0:0]
		for _, a := range found {
			key := projectKey{a.ProjectID, a.Key}
			if wanted[key] && !have[key] {
				have[key] = true
				level = append(level, a)
			}
		}
		result = append(result, level...)
	}
	return result, nil
}

// regions sums the region rows by provider and region, most assets first.
func (s *Service) regions(ids []uuid.UUID, projects map[uuid.UUID]string) ([]RegionCountDTO, error) {
	rows, err := s.repo.regions(ids)
	if err != nil {
		return nil, err
	}
	byKey := map[[2]string]*RegionCountDTO{}
	result := []RegionCountDTO{}
	for _, row := range rows {
		key := [2]string{row.Provider, row.Region}
		region := byKey[key]
		if region == nil {
			region = &RegionCountDTO{Provider: row.Provider, Region: row.Region, Projects: []string{}}
			byKey[key] = region
		}
		region.Assets += row.Count
		switch row.Health {
		case HealthWarning:
			region.Warning += row.Count
		case HealthCritical:
			region.Critical += row.Count
		}
		if name := projects[row.ProjectID]; !slices.Contains(region.Projects, name) {
			region.Projects = append(region.Projects, name)
		}
	}
	for _, region := range byKey {
		slices.Sort(region.Projects)
		result = append(result, *region)
	}
	slices.SortFunc(result, func(a, b RegionCountDTO) int {
		if a.Assets != b.Assets {
			return cmp.Compare(b.Assets, a.Assets)
		}
		return cmp.Compare(a.Provider+" "+a.Region, b.Provider+" "+b.Region)
	})
	return result, nil
}

// history is the projects' hourly counts over the last historyDays, and those of about a day ago.
func (s *Service) history(ids []uuid.UUID) (*HistoryDTO, error) {
	now := s.now()
	rows, err := s.repo.counts(ids, now.Add(-historyDays*24*time.Hour).Truncate(time.Hour))
	if err != nil {
		return nil, err
	}
	history := &HistoryDTO{Hours: []time.Time{}, Kinds: map[string][]int64{}}
	index := map[time.Time]int{}
	for _, row := range rows {
		if row.Kind == "" {
			index[row.Hour.UTC()] = len(history.Hours)
			history.Hours = append(history.Hours, row.Hour.UTC())
		}
	}
	target := now.Add(-previousAge).Truncate(time.Hour)
	var previous time.Time
	for _, hour := range history.Hours {
		if !hour.After(target) && !hour.Before(target.Add(-previousSlack)) {
			previous = hour
		}
	}
	for _, row := range rows {
		i, ok := index[row.Hour.UTC()]
		if row.Kind == "" || !ok {
			continue
		}
		if history.Kinds[row.Kind] == nil {
			history.Kinds[row.Kind] = make([]int64, len(history.Hours))
		}
		history.Kinds[row.Kind][i] += row.Count
		if !previous.IsZero() && row.Hour.UTC().Equal(previous) {
			if history.Previous == nil {
				history.Previous = map[string]map[string]int64{}
			}
			if history.Previous[row.Kind] == nil {
				history.Previous[row.Kind] = map[string]int64{}
			}
			history.Previous[row.Kind][row.Health] += row.Count
		}
	}
	if !previous.IsZero() {
		history.PreviousAt = &previous
		if history.Previous == nil {
			history.Previous = map[string]map[string]int64{}
		}
	}
	return history, nil
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
