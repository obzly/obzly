// Package authz is the one place resource access is decided: Authorize for code,
// Require for routes. A user's project role is the strongest of their own
// membership and their teams' grants; a global admin may do anything. Denials
// are audited and every decision is counted.
package authz

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"obzly/internal/features/audit_logs"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/telemetry"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Action is what a subject wants to do; each needs a minimum role.
type Action string

const (
	// ProjectRead: see a project's data and settings (any role).
	ProjectRead Action = "project.read"
	// ProjectWrite: change its data - dashboards, silences (not viewers).
	ProjectWrite Action = "project.write"
	// ProjectManage: change how it works - rules, keys, members (admins).
	ProjectManage Action = "project.manage"
	// ProjectOwn: hold its credentials - notification channels (owner).
	ProjectOwn Action = "project.own"
)

// Resource is what an action is on.
type Resource struct {
	Kind string
	ID   uuid.UUID
}

// Decision is Authorize's answer; Role is the subject's effective role
// (an owner's for a global admin), nil without one.
type Decision struct {
	Allowed bool
	Role    *users_enums.ProjectRole
	Reason  string
}

const (
	kindProject     = "project"
	decisionKey     = "authzDecision"
	denialAuditTTL  = time.Minute
	maxDenialMemory = 10_000
	// maxUserDenialAudits bounds one user's audited denials a minute: each
	// key is new on a random project id, so the key alone doesn't.
	maxUserDenialAudits = 20
)

var (
	minRank = map[Action]int{ProjectRead: 1, ProjectWrite: 2, ProjectManage: 3, ProjectOwn: 4}
	rank    = map[users_enums.ProjectRole]int{
		users_enums.ProjectRoleViewer: 1,
		users_enums.ProjectRoleMember: 2,
		users_enums.ProjectRoleAdmin:  3,
		users_enums.ProjectRoleOwner:  4,
	}
	memberships = &projects_repositories.MembershipRepository{}

	deniedMu    sync.Mutex
	denied      = map[string]time.Time{}
	userDenials = map[uuid.UUID]*denialWindow{}
)

type denialWindow struct {
	start time.Time
	count int
}

// Project names a project.
func Project(id uuid.UUID) Resource { return Resource{Kind: kindProject, ID: id} }

// Authorize decides whether user may do action on resource. An error is a
// failed lookup, never a denial.
func Authorize(_ context.Context, user *users_models.User, action Action, resource Resource) (Decision, error) {
	need, ok := minRank[action]
	if !ok || resource.Kind != kindProject {
		return Decision{}, fmt.Errorf("authz: unknown action %q on %q", action, resource.Kind)
	}
	if user == nil {
		return record(nil, action, resource, Decision{Reason: "unauthenticated"}), nil
	}
	if user.Role == users_enums.UserRoleAdmin {
		owner := users_enums.ProjectRoleOwner
		return record(user, action, resource, Decision{Allowed: true, Role: &owner, Reason: "global admin"}), nil
	}
	role, err := memberships.GetUserProjectRole(resource.ID, user.ID)
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{Role: role, Reason: "no role on the project"}
	if role != nil {
		decision.Allowed = rank[*role] >= need
		decision.Reason = "role " + string(*role)
	}
	return record(user, action, resource, decision), nil
}

// Require gates a /projects/:id/... route on action; the handler can read
// the decision with FromContext.
func Require(action Action) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		user, ok := users_middleware.GetUserFromContext(ctx)
		if !ok {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
			return
		}
		projectID, err := uuid.Parse(ctx.Param("id"))
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid project ID"})
			return
		}
		decision, err := Authorize(ctx.Request.Context(), user, action, Project(projectID))
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to check permissions"})
			return
		}
		if !decision.Allowed {
			ctx.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			return
		}
		ctx.Set(decisionKey, decision)
		ctx.Next()
	}
}

// FromContext is the decision Require made for this request.
func FromContext(ctx *gin.Context) (Decision, bool) {
	value, ok := ctx.Get(decisionKey)
	if !ok {
		return Decision{}, false
	}
	decision, ok := value.(Decision)
	return decision, ok
}

func record(user *users_models.User, action Action, resource Resource, decision Decision) Decision {
	result := "allow"
	if !decision.Allowed {
		result = "deny"
		auditDenial(user, action, resource, decision.Reason)
	}
	telemetry.AuthzDecisions.WithLabelValues(string(action), result).Inc()
	return decision
}

// auditDenial writes a denial once a minute per subject, action and
// resource, and at most maxUserDenialAudits a minute per subject, so a
// client retrying a 403 or probing random ids can't flood the log. The
// metric still counts every denial.
func auditDenial(user *users_models.User, action Action, resource Resource, reason string) {
	if user == nil {
		return
	}
	key := user.ID.String() + "|" + string(action) + "|" + resource.ID.String()
	now := time.Now().UTC()
	deniedMu.Lock()
	if last, seen := denied[key]; seen && now.Sub(last) < denialAuditTTL {
		deniedMu.Unlock()
		return
	}
	if len(denied) >= maxDenialMemory || len(userDenials) >= maxDenialMemory {
		forgetExpiredDenials(now)
	}
	window := userDenials[user.ID]
	if window == nil || now.Sub(window.start) >= denialAuditTTL {
		if len(userDenials) >= maxDenialMemory {
			deniedMu.Unlock()
			return
		}
		window = &denialWindow{start: now}
		userDenials[user.ID] = window
	}
	window.count++
	if window.count > maxUserDenialAudits || len(denied) >= maxDenialMemory {
		deniedMu.Unlock()
		if window.count == maxUserDenialAudits+1 {
			audit_logs.GetAuditLogService().WriteAuditLog(
				fmt.Sprintf("Access denied %d times within a minute; further denials not logged until %s",
					maxUserDenialAudits+1, window.start.Add(denialAuditTTL).Format(time.RFC3339)),
				&user.ID,
				nil,
			)
		}
		return
	}
	denied[key] = now
	deniedMu.Unlock()

	// audit_logs.project_id references projects: a denial on one that
	// doesn't exist names it in the message only.
	var projectID *uuid.UUID
	var exists bool
	if storage.GetDb().
		Raw("SELECT EXISTS (SELECT 1 FROM projects WHERE id = ?)", resource.ID).
		Scan(&exists).
		Error == nil &&
		exists {
		projectID = &resource.ID
	}
	audit_logs.GetAuditLogService().WriteAuditLog(
		fmt.Sprintf("Access denied: %s on %s %s (%s)", action, resource.Kind, resource.ID, reason),
		&user.ID,
		projectID,
	)
}

// forgetExpiredDenials drops entries older than denialAuditTTL; the caller
// holds deniedMu.
func forgetExpiredDenials(now time.Time) {
	for k, at := range denied {
		if now.Sub(at) >= denialAuditTTL {
			delete(denied, k)
		}
	}
	for id, window := range userDenials {
		if now.Sub(window.start) >= denialAuditTTL {
			delete(userDenials, id)
		}
	}
}
