package users_services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"obzly/internal/cache"
	"obzly/internal/config"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// ErrCentralExchangeDisabled is returned (as 404) when no CONTROL_PLANE_ISSUER is
// configured, so the endpoint looks absent on a standalone install.
var ErrCentralExchangeDisabled = errors.New("central token exchange is not configured")

// ErrCentralExchangeForbidden is returned when a valid assertion grants neither
// global ADMIN nor any project; no user or session is created.
var ErrCentralExchangeForbidden = errors.New("this account has no access to this site")

// ErrCentralExchangeReplayed is returned for an already-consumed jti; assertions are single-use.
var ErrCentralExchangeReplayed = errors.New("assertion has already been used")

// ErrCentralExchangeAccountInactive: the local account is deactivated on this
// Site, which wins over anything the control plane says.
var ErrCentralExchangeAccountInactive = errors.New("this account has been deactivated on this site")

// ErrCentralExchangeAccountNotLinkable is returned when link-by-email would hit
// the root admin or an account with its own sign-in: a compromised control plane
// must not be able to claim those.
var ErrCentralExchangeAccountNotLinkable = errors.New(
	"this site has a local account with the same email that can't be used through a control plane " +
		"(its root admin, an admin account without a matching ADMIN grant, or an account with its own " +
		"password or SSO sign-in) - use a central account with a different email, or ask a Site admin " +
		"to remove the local account",
)

const (
	// centralExchangeTokenTTL is far shorter than accessTokenTTL: the control plane
	// re-exchanges before expiry, so this only bounds a captured Site token.
	centralExchangeTokenTTL = time.Hour

	// centralAssertionJTIPrefix namespaces the replay guard's Valkey keys.
	centralAssertionJTIPrefix = "central_assertion_jti:"
	centralAssertionJTIMinTTL = 3 * time.Minute
)

// centralIdentityRepositoryInterface exists so tests can build a UserService
// without a database; production wires the concrete repository.
type centralIdentityRepositoryInterface interface {
	GetUserByIdentity(issuer, subject string) (*users_models.User, error)
	LinkIdentity(userID uuid.UUID, issuer, subject string) error
}

var _ centralIdentityRepositoryInterface = (*users_repositories.CentralIdentityRepository)(nil)

// claimAssertionJTIScript checks-and-marks atomically, so two concurrent
// replays can't both see "not used yet".
const claimAssertionJTIScript = `
local key = KEYS[1]
local ttl_seconds = tonumber(ARGV[1])
if redis.call('EXISTS', key) == 1 then
    return 0
end
redis.call('SET', key, '1', 'EX', ttl_seconds)
return 1
`

// claimAssertionJTI returns true when jti was fresh, false on replay.
func claimAssertionJTI(jti string, ttl time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := cache.GetCache()
	result := client.Do(ctx, client.B().Eval().
		Script(claimAssertionJTIScript).
		Numkeys(1).
		Key(centralAssertionJTIPrefix+jti).
		Arg(fmt.Sprintf("%d", int64(ttl.Seconds()))).
		Build())
	if result.Error() != nil {
		return false, fmt.Errorf("failed to check assertion replay: %w", result.Error())
	}

	claimed, err := result.AsInt64()
	if err != nil {
		return false, fmt.Errorf("failed to parse assertion replay check: %w", err)
	}
	return claimed == 1, nil
}

// assertionJTITTL keeps a jti until the assertion could no longer verify:
// a fixed TTL shorter than the accepted lifetime would let it replay.
func assertionJTITTL(claims *centralAssertionClaims, now time.Time) time.Duration {
	ttl := time.Unix(claims.Expiry, 0).Sub(now) + centralAssertionMaxSkew
	return max(ttl, centralAssertionJTIMinTTL)
}

// checkCentralEgressAllowed enforces CONTROL_PLANE_EGRESS_CIDRS when set:
// defense in depth on top of the assertion signature.
func checkCentralEgressAllowed(env config.EnvVariables, clientIP string) error {
	if env.ControlPlaneEgressCIDRs == "" {
		return nil
	}
	networks, err := config.ParseCIDRList(env.ControlPlaneEgressCIDRs)
	if err != nil {
		// Validated at startup; fail closed if it somehow doesn't parse.
		return fmt.Errorf("invalid CONTROL_PLANE_EGRESS_CIDRS: %w", err)
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		return errors.New("could not determine caller's IP address")
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return nil
		}
	}
	return errors.New("caller's IP address is not an allowed control-plane egress address")
}

// ExchangeCentralAssertion is the Site side of the control plane's token
// exchange (ARCHITECTURE.md "Multi-site: control plane and Sites"). It verifies
// the assertion, resolves or provisions the user, reconciles grants and mints
// a short-lived local session.
func (s *UserService) ExchangeCentralAssertion(rawAssertion, clientIP string) (*users_dto.SignInResponseDTO, error) {
	return s.exchangeCentralAssertion(config.GetEnv(), rawAssertion, clientIP)
}

// exchangeCentralAssertion takes env explicitly so tests can use a fake
// control plane without the process-wide config.
func (s *UserService) exchangeCentralAssertion(
	env config.EnvVariables,
	rawAssertion, clientIP string,
) (*users_dto.SignInResponseDTO, error) {
	if !env.SiteExchangeEnabled() {
		return nil, ErrCentralExchangeDisabled
	}

	if err := checkCentralEgressAllowed(env, clientIP); err != nil {
		return nil, err
	}

	// IP-keyed limit before any crypto, so garbage assertions can't burn CPU.
	if clientIP != "" {
		limited, err := loginRateLimited("exchange-ip:" + clientIP)
		if err != nil {
			return nil, fmt.Errorf("failed to check exchange rate limit: %w", err)
		}
		if limited {
			return nil, ErrLoginRateLimited
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	claims, err := verifyCentralAssertion(ctx, env, rawAssertion)
	if err != nil {
		if clientIP != "" {
			if _, rerr := recordLoginFailure("exchange-ip:" + clientIP); rerr != nil {
				logger.GetLogger().Error("failed to record exchange failure", "error", rerr)
			}
		}
		return nil, fmt.Errorf("invalid assertion: %w", err)
	}

	if claims.Subject == "" || claims.Email == "" || claims.JTI == "" {
		return nil, errors.New("assertion is missing a required claim")
	}

	// Per-subject limit catches a compromised control-plane key minting many
	// assertions for one identity.
	subjectLimited, err := loginRateLimited("exchange-sub:" + claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to check exchange rate limit: %w", err)
	}
	if subjectLimited {
		return nil, ErrLoginRateLimited
	}

	// Only replays and bad signatures count toward lockout. Policy refusals are
	// valid answers; counting them locked the control plane's single IP out for
	// every user.
	recordFailure := func() {
		if _, err := recordLoginFailure("exchange-sub:" + claims.Subject); err != nil {
			logger.GetLogger().Error("failed to record exchange failure", "error", err)
		}
		if clientIP != "" {
			if _, err := recordLoginFailure("exchange-ip:" + clientIP); err != nil {
				logger.GetLogger().Error("failed to record exchange failure", "error", err)
			}
		}
	}

	fresh, err := claimAssertionJTI(claims.JTI, assertionJTITTL(claims, time.Now()))
	if err != nil {
		return nil, err
	}
	if !fresh {
		recordFailure()
		return nil, ErrCentralExchangeReplayed
	}

	if claims.SiteRole != string(users_enums.UserRoleAdmin) && len(claims.Projects) == 0 &&
		allProjectsRoleOf(claims) == "" {
		return nil, ErrCentralExchangeForbidden
	}

	user, err := s.resolveOrProvisionCentralUser(env, claims)
	if err != nil {
		return nil, err
	}

	if !user.IsActiveUser() {
		return nil, ErrCentralExchangeAccountInactive
	}

	if err := s.reconcileCentralRole(user, claims); err != nil {
		return nil, fmt.Errorf("failed to reconcile central role: %w", err)
	}
	if refreshed, err := s.userRepository.GetUserByID(user.ID); err == nil && refreshed != nil {
		// The minted token's role claim must reflect the reconciled role.
		user = refreshed
	}

	s.syncCentralMemberships(user.ID, claims)

	response, err := s.GenerateAccessTokenWithOptions(user, AccessTokenOptions{
		TTL: centralExchangeTokenTTL,
		ExtraClaims: map[string]any{
			AuthOriginClaim: AuthOriginControlPlane,
			"amr":           claims.AMR,
			"cp_iss":        env.ControlPlaneIssuer,
			"cp_sub":        claims.Subject,
		},
	})
	if err != nil {
		return nil, err
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("User signed in via control-plane token exchange", &user.ID, nil)
	}

	return response, nil
}

// resolveOrProvisionCentralUser maps the assertion subject to a local user: a
// linked identity, then (if CONTROL_PLANE_LINK_BY_EMAIL) a verified email match
// other than the root admin, else a new MEMBER. Not gated by open registration:
// the control plane already authenticated the user.
func (s *UserService) resolveOrProvisionCentralUser(
	env config.EnvVariables,
	claims *centralAssertionClaims,
) (*users_models.User, error) {
	existing, err := s.centralIdentityRepository.GetUserByIdentity(env.ControlPlaneIssuer, claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to check central identity: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	if env.LinkByEmail() {
		userByEmail, err := s.userRepository.GetUserByEmail(claims.Email)
		if err != nil {
			return nil, fmt.Errorf("failed to check email: %w", err)
		}
		if userByEmail != nil {
			if userByEmail.IsRootAdmin {
				return nil, ErrCentralExchangeAccountNotLinkable
			}
			// A local ADMIN may only be claimed by an assertion granting ADMIN here;
			// otherwise a viewer could set their central email to an admin's and take over.
			if userByEmail.Role == users_enums.UserRoleAdmin && claims.SiteRole != string(users_enums.UserRoleAdmin) {
				return nil, ErrCentralExchangeAccountNotLinkable
			}
			// The central email is unverified, so an account with its own sign-in is never
			// claimed by email.
			hasOwnSignIn, err := s.hasOwnSignIn(userByEmail)
			if err != nil {
				return nil, err
			}
			if hasOwnSignIn {
				return nil, ErrCentralExchangeAccountNotLinkable
			}
			if err := s.centralIdentityRepository.LinkIdentity(
				userByEmail.ID, env.ControlPlaneIssuer, claims.Subject,
			); err != nil {
				return nil, fmt.Errorf("failed to link central identity: %w", err)
			}
			return userByEmail, nil
		}
	}

	basicPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
	if err != nil {
		return nil, fmt.Errorf("failed to get default plan: %w", err)
	}
	var planID *uuid.UUID
	if basicPlan != nil {
		planID = &basicPlan.ID
	}

	name := claims.Name
	if name == "" {
		name = claims.Email
	}

	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                claims.Email,
		Name:                 name,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		PlanID:               planID,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
	if err := s.userRepository.CreateUser(user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	if err := s.centralIdentityRepository.LinkIdentity(user.ID, env.ControlPlaneIssuer, claims.Subject); err != nil {
		return nil, fmt.Errorf("failed to link central identity: %w", err)
	}
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User provisioned via control-plane exchange: %s", user.Email),
			&user.ID,
			nil,
		)
	}
	return user, nil
}

func (s *UserService) hasOwnSignIn(user *users_models.User) (bool, error) {
	if user.HasPassword() || user.GitHubOAuthID != nil || user.GoogleOAuthID != nil {
		return true, nil
	}
	if s.ssoIdentityRepository == nil {
		return false, nil
	}
	linked, err := s.ssoIdentityRepository.HasAnyIdentity(user.ID)
	if err != nil {
		return false, fmt.Errorf("failed to check SSO identities: %w", err)
	}
	return linked, nil
}

// reconcileCentralRole promotes per site_role and demotes only a centrally
// granted admin; manual or SSO admins are never touched.
func (s *UserService) reconcileCentralRole(user *users_models.User, claims *centralAssertionClaims) error {
	if claims.SiteRole == string(users_enums.UserRoleAdmin) {
		if user.Role != users_enums.UserRoleAdmin {
			if _, err := s.userRepository.GrantCentralAdmin(user.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if user.CentralAdmin {
		if _, err := s.userRepository.RevokeCentralAdmin(user.ID); err != nil {
			return err
		}
	}
	return nil
}

// allProjectsRoleOf accepts only PROJECT_MEMBER / PROJECT_VIEWER; Site-wide
// admin is site_role's job.
func allProjectsRoleOf(claims *centralAssertionClaims) users_enums.ProjectRole {
	switch role := users_enums.ProjectRole(claims.AllProjectsRole); role {
	case users_enums.ProjectRoleMember, users_enums.ProjectRoleViewer:
		return role
	default:
		return ""
	}
}

// syncCentralMemberships logs errors rather than failing sign-in, so one bad
// project doesn't block the rest.
func (s *UserService) syncCentralMemberships(userID uuid.UUID, claims *centralAssertionClaims) {
	if s.groupMembershipSyncer == nil {
		return
	}

	desired := make(map[uuid.UUID]users_enums.ProjectRole, len(claims.Projects))
	for _, project := range claims.Projects {
		projectID, err := uuid.Parse(project.ID)
		if err != nil {
			logger.GetLogger().Warn(
				"control-plane sync: skipping unparsable project id", "projectId", project.ID,
			)
			continue
		}
		role := users_enums.ProjectRole(project.Role)
		if !role.IsValid() || role == users_enums.ProjectRoleOwner {
			logger.GetLogger().Warn(
				"control-plane sync: skipping invalid/disallowed role",
				"projectId", project.ID, "role", project.Role,
			)
			continue
		}
		desired[projectID] = role
	}

	if err := s.groupMembershipSyncer.SyncCentralMemberships(userID, desired, allProjectsRoleOf(claims)); err != nil {
		logger.GetLogger().Error("control-plane membership sync failed", "userId", userID, "error", err)
	}
}
