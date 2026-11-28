package projects_services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"obzly/internal/features/audit_logs"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

var (
	ErrRetentionPolicyForbidden = errors.New("insufficient permissions to change retention policies")
	ErrRetentionPolicyNotFound  = errors.New("insufficient permissions to view project")
)

const (
	// logRulesTTL: how long ingest reuses a project's log sampling rules; a
	// change reaches every replica within it.
	logRulesTTL = 30 * time.Second
	// logRulesTimeout, logRulesRetry: a hung database held every log request
	// (each on its own pool connection) until it answered, warm replicas too
	// once the rules expired. A failed read now keeps the last rules (none:
	// keep everything) and is retried after logRulesRetry.
	logRulesTimeout = 3 * time.Second
	logRulesRetry   = 10 * time.Second
)

// RetentionPolicyService manages per-signal retention and sampling.
type RetentionPolicyService struct {
	projectService  *ProjectService
	repository      *projects_repositories.RetentionPolicyRepository
	auditLogService *audit_logs.AuditLogService
	logRules        sync.Map // uuid.UUID -> cachedLogRules
	logRulesReads   singleflight.Group
}

type cachedLogRules struct {
	rules   projects_models.LogSampleRules
	fetched time.Time
}

// GetRetentionPolicies is a project's policy for every signal, defaults
// where none is set; any project member may read them.
func (s *RetentionPolicyService) GetRetentionPolicies(
	projectID uuid.UUID, user *users_models.User,
) ([]projects_models.RetentionPolicy, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canAccess {
		return nil, ErrRetentionPolicyNotFound
	}
	bySignal, err := s.PoliciesFor(projectID)
	if err != nil {
		return nil, err
	}
	policies := make([]projects_models.RetentionPolicy, 0, len(projects_models.Signals))
	for _, signal := range projects_models.Signals {
		policies = append(policies, bySignal[signal])
	}
	return policies, nil
}

// SetRetentionPolicies replaces the given signals' policies; project
// owners and admins only.
func (s *RetentionPolicyService) SetRetentionPolicies(
	projectID uuid.UUID, policies []projects_models.RetentionPolicy, user *users_models.User,
) ([]projects_models.RetentionPolicy, error) {
	canManage, err := s.projectService.CanUserManageProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canManage {
		return nil, ErrRetentionPolicyForbidden
	}
	seen := map[string]bool{}
	signals := make([]string, 0, len(policies))
	for i := range policies {
		policies[i].ProjectID = projectID
		if policies[i].SampleRules == nil {
			policies[i].SampleRules = projects_models.LogSampleRules{}
		}
		if err := policies[i].Validate(); err != nil {
			return nil, err
		}
		if seen[policies[i].Signal] {
			return nil, fmt.Errorf("signal %q given twice", policies[i].Signal)
		}
		seen[policies[i].Signal] = true
		signals = append(signals, policies[i].Signal)
	}
	if err := s.repository.Upsert(policies); err != nil {
		return nil, fmt.Errorf("failed to save retention policies: %w", err)
	}
	s.logRules.Delete(projectID)
	s.auditLogService.WriteAuditLog(
		"Retention policies updated: "+strings.Join(signals, ", "), &user.ID, &projectID,
	)
	return s.GetRetentionPolicies(projectID, user)
}

// LogSampleRules is projectID's log sampling rules for ingest, cached for
// logRulesTTL; the last rules read (none: keep everything) when they can't
// be read.
func (s *RetentionPolicyService) LogSampleRules(projectID uuid.UUID) projects_models.LogSampleRules {
	var cached cachedLogRules
	if value, ok := s.logRules.Load(projectID); ok {
		cached, _ = value.(cachedLogRules)
		if time.Since(cached.fetched) < logRulesTTL {
			return cached.rules
		}
	}
	rules, _, _ := s.logRulesReads.Do(projectID.String(), func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), logRulesTimeout)
		defer cancel()
		stored, err := s.repository.ListByProjectContext(ctx, projectID)
		if err != nil {
			retryAt := time.Now().UTC().Add(logRulesRetry - logRulesTTL)
			s.logRules.Store(projectID, cachedLogRules{rules: cached.rules, fetched: retryAt})
			return cached.rules, nil
		}
		rules := withDefaults(projectID, stored)[projects_models.SignalLogs].SampleRules
		s.logRules.Store(projectID, cachedLogRules{rules: rules, fetched: time.Now().UTC()})
		return rules, nil
	})
	result, _ := rules.(projects_models.LogSampleRules)
	return result
}

// PoliciesFor is projectID's policy per signal, defaults filled in, for
// background work (no permission check).
func (s *RetentionPolicyService) PoliciesFor(projectID uuid.UUID) (map[string]projects_models.RetentionPolicy, error) {
	stored, err := s.repository.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read retention policies: %w", err)
	}
	return withDefaults(projectID, stored), nil
}

// AllPolicies is every project's policies by project id, defaults filled
// in for the projects that have any; a project without one gets
// DefaultRetentionPolicy from PolicyOf.
func (s *RetentionPolicyService) AllPolicies() (map[uuid.UUID]map[string]projects_models.RetentionPolicy, error) {
	stored, err := s.repository.ListAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read retention policies: %w", err)
	}
	byProject := map[uuid.UUID][]projects_models.RetentionPolicy{}
	for _, policy := range stored {
		byProject[policy.ProjectID] = append(byProject[policy.ProjectID], policy)
	}
	result := make(map[uuid.UUID]map[string]projects_models.RetentionPolicy, len(byProject))
	for projectID, policies := range byProject {
		result[projectID] = withDefaults(projectID, policies)
	}
	return result, nil
}

// PolicyOf is signal's policy for projectID from an AllPolicies result.
func PolicyOf(
	all map[uuid.UUID]map[string]projects_models.RetentionPolicy, projectID uuid.UUID, signal string,
) projects_models.RetentionPolicy {
	if policy, ok := all[projectID][signal]; ok {
		return policy
	}
	return projects_models.DefaultRetentionPolicy(projectID, signal)
}

func withDefaults(
	projectID uuid.UUID, stored []projects_models.RetentionPolicy,
) map[string]projects_models.RetentionPolicy {
	bySignal := make(map[string]projects_models.RetentionPolicy, len(projects_models.Signals))
	for _, signal := range projects_models.Signals {
		bySignal[signal] = projects_models.DefaultRetentionPolicy(projectID, signal)
	}
	for _, policy := range stored {
		bySignal[policy.Signal] = policy
	}
	return bySignal
}
