package users_services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
	"gorm.io/gorm"

	"obzly/internal/config"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_interfaces "obzly/internal/features/users/interfaces"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_repositories "obzly/internal/features/users/sso/repositories"
	sso_services "obzly/internal/features/users/sso/services"
	"obzly/internal/util/logger"
)

// ErrInvalidInviteToken is deliberately generic (wrong, expired or missing token)
// so it can't be used to enumerate which emails have pending invites.
var ErrInvalidInviteToken = errors.New("invalid or expired invite")

// inviteTokenTTL: long enough for an admin to relay the link, short enough
// that a forgotten or leaked link expires.
const inviteTokenTTL = 14 * 24 * time.Hour

// generateInviteToken returns a random token and its SHA-256 hash: an invite
// token is a bearer secret, not a user-chosen password, so bcrypt isn't needed.
func generateInviteToken() (raw, hash string, err error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(tokenBytes)
	return raw, hashInviteToken(raw), nil
}

func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func generateBootstrapToken() (raw, hash string, err error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(tokenBytes)
	return raw, hashBootstrapToken(raw), nil
}

func hashBootstrapToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ErrSSOEmailNotVerified covers every OAuth/SSO provider, so the message stays
// generic (only portal connectors have a "require verified email" toggle).
var ErrSSOEmailNotVerified = errors.New(
	"the identity provider has not verified this email address, so it can't be used to sign in; " +
		"verify it with your identity provider and try again",
)

// ErrSSONoMappedGroup is returned on an SSO user's first sign-in when open
// registration is off and none of their IdP groups is mapped to any access.
var ErrSSONoMappedGroup = errors.New(
	"your account isn't in a group that has access here, and sign-up is closed - " +
		"ask an administrator to map your group or invite you",
)

// ErrOAuthAccountNotLinkable is returned when an OAuth/SSO email matches an
// account that has its own sign-in method or is an admin (see getOrCreateUserFromOAuth).
var ErrOAuthAccountNotLinkable = errors.New(
	"an account with this email already exists; sign in with your password (or the provider " +
		"you originally used) instead",
)

var errOAuthAccountDeactivated = errors.New("user account is deactivated")

type UserService struct {
	userRepository        *users_repositories.UserRepository
	secretKeyRepository   *users_repositories.SecretKeyRepository
	userPlanRepository    *users_repositories.UserPlanRepository
	settingsService       *SettingsService
	auditLogWriter        users_interfaces.AuditLogWriter
	ssoIdentityRepository *sso_repositories.IdentityRepository
	groupMembershipSyncer users_interfaces.GroupMembershipSyncer
	// Site-side token exchange (ARCHITECTURE.md "Multi-site: control plane and Sites").
	centralIdentityRepository centralIdentityRepositoryInterface
	// Set only on a control plane. Last, so the positional literal in di.go can omit it.
	ssoSiteGrantSyncer users_interfaces.SSOSiteGrantSyncer
}

func (s *UserService) SetGroupMembershipSyncer(syncer users_interfaces.GroupMembershipSyncer) {
	s.groupMembershipSyncer = syncer
}

// SetSSOSiteGrantSyncer is called only on a control plane.
func (s *UserService) SetSSOSiteGrantSyncer(syncer users_interfaces.SSOSiteGrantSyncer) {
	s.ssoSiteGrantSyncer = syncer
}

func (s *UserService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

// ErrSignUpRateLimited means too many sign-up attempts from one IP.
var ErrSignUpRateLimited = errors.New("too many sign-up attempts - please wait a while and try again")

// errRegistrationDisabled is also what an existing email gets while
// registration is closed, so the answer doesn't reveal which emails exist.
var errRegistrationDisabled = errors.New("external registration is disabled")

// SignUp runs every cheap check (rate limit, invite token, registration
// policy, existing account) before the deliberately slow bcrypt hash.
func (s *UserService) SignUp(request *users_dto.SignUpRequestDTO, clientIP string) error {
	if clientIP != "" {
		limited, err := countRequest("signup-ip:"+clientIP, maxSignUpsPerIP, signUpWindow)
		if err != nil {
			return fmt.Errorf("failed to check sign-up rate limit: %w", err)
		}
		if limited {
			return ErrSignUpRateLimited
		}
	}

	existingUser, err := s.userRepository.GetUserByEmail(request.Email)
	if err != nil {
		return fmt.Errorf("failed to check existing user: %w", err)
	}

	// Activating an invited account requires its invite token, so knowing the
	// email alone can't claim it.
	if existingUser != nil && existingUser.Status == users_enums.UserStatusInvited {
		if !validInviteToken(existingUser, request.InviteToken) {
			return ErrInvalidInviteToken
		}

		hashedPasswordStr, err := hashPassword(request.Password)
		if err != nil {
			return err
		}

		if err := s.userRepository.UpdateUserPassword(existingUser.ID, hashedPasswordStr); err != nil {
			return fmt.Errorf("failed to set password: %w", err)
		}

		if err := s.userRepository.UpdateUserStatus(existingUser.ID, users_enums.UserStatusActive); err != nil {
			return fmt.Errorf("failed to activate user: %w", err)
		}

		if err := s.userRepository.ClearInviteToken(existingUser.ID); err != nil {
			return fmt.Errorf("failed to clear invite token: %w", err)
		}

		name := request.Name
		if err := s.userRepository.UpdateUserInfo(existingUser.ID, &name, nil); err != nil {
			return fmt.Errorf("failed to update name: %w", err)
		}

		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("Invited user completed registration: %s", existingUser.Email),
			&existingUser.ID,
			nil,
		)

		return nil
	}

	settings, err := s.settingsService.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}

	if !settings.IsAllowExternalRegistrations {
		return errRegistrationDisabled
	}

	if existingUser != nil {
		return errors.New("user with this email already exists")
	}

	basicPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
	if err != nil {
		return fmt.Errorf("failed to get default plan: %w", err)
	}

	var planID *uuid.UUID
	if basicPlan != nil {
		planID = &basicPlan.ID
	}

	hashedPasswordStr, err := hashPassword(request.Password)
	if err != nil {
		return err
	}

	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                request.Email,
		Name:                 request.Name,
		HashedPassword:       &hashedPasswordStr,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		PlanID:               planID,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}

	if err := s.userRepository.CreateUser(user); err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	s.auditLogWriter.WriteAuditLog(
		fmt.Sprintf("User registered with email: %s", user.Email),
		&user.ID,
		nil,
	)

	return nil
}

func hashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hashed), nil
}

// validInviteToken compares in constant time and checks expiry. A user with no
// stored token always fails; an admin can still set their password directly.
func validInviteToken(user *users_models.User, providedToken string) bool {
	if providedToken == "" || user.InviteTokenHash == nil || user.InviteTokenExpiresAt == nil {
		return false
	}
	if time.Now().After(*user.InviteTokenExpiresAt) {
		return false
	}
	providedHash := hashInviteToken(providedToken)
	return subtle.ConstantTimeCompare([]byte(providedHash), []byte(*user.InviteTokenHash)) == 1
}

// errInvalidCredentials is the one message for unknown email, no password and
// wrong password, so responses don't reveal which emails have accounts.
var errInvalidCredentials = errors.New("invalid email or password")

// ErrAuthUnavailable: a token couldn't be checked for an infrastructure
// reason (Valkey or the database unreachable), not because it's bad. The
// middleware answers 503 so the UI retries instead of signing the user out.
var ErrAuthUnavailable = errors.New("authentication temporarily unavailable")

// dummyPasswordHash is a valid bcrypt hash of an unused plaintext, compared
// when there is no real hash so every SignIn path costs the same time.
const dummyPasswordHash = "$2a$10$XlfgBFRsOezdwzikjO1G6uW2is5pPHFD2OEZVcUrb338puqSIOrDS"

// SignIn checks per-IP and per-email failure counters before the password, so
// one client can't lock out everyone and one account can't be brute-forced.
func (s *UserService) SignIn(
	request *users_dto.SignInRequestDTO,
	clientIP string,
) (*users_dto.SignInResponseDTO, error) {
	// Keyed the way the lookup normalises, so case or spaces can't reset it.
	normalizedEmail := strings.ToLower(strings.TrimSpace(request.Email))
	emailKey := "email:" + normalizedEmail
	emailLimited, err := loginRateLimited(emailKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check login rate limit: %w", err)
	}
	ipLimited := false
	if clientIP != "" {
		ipLimited, err = loginRateLimited("ip:" + clientIP)
		if err != nil {
			return nil, fmt.Errorf("failed to check login rate limit: %w", err)
		}
	}
	if emailLimited || ipLimited {
		return nil, ErrLoginRateLimited
	}

	// Reports whether this failure crossed the threshold, so that same request
	// already gets ErrLoginRateLimited (same shape as mfa_services.verifyCode).
	recordFailure := func() bool {
		justLockedOut := false
		if locked, err := recordLoginFailure(emailKey); err != nil {
			logger.GetLogger().Error("failed to record login failure", "error", err)
		} else if locked {
			justLockedOut = true
		}
		if clientIP != "" {
			if locked, err := recordLoginFailure("ip:" + clientIP); err != nil {
				logger.GetLogger().Error("failed to record login failure", "error", err)
			} else if locked {
				justLockedOut = true
			}
		}
		if justLockedOut && s.auditLogWriter != nil {
			s.auditLogWriter.WriteAuditLog(
				fmt.Sprintf(
					"Sign-in locked after too many failed attempts (email %s, IP %s)",
					normalizedEmail,
					clientIP,
				),
				nil,
				nil,
			)
		}
		return justLockedOut
	}

	user, err := s.userRepository.GetUserByEmail(request.Email)
	if err != nil {
		// A lookup error is treated as "no such user" so responses don't differ.
		user = nil
	}

	// Always run a real bcrypt comparison (dummy hash when there is no password)
	// so response time doesn't reveal whether the email exists.
	hashToCompare := dummyPasswordHash
	knownAccount := user != nil && user.HasPassword()
	if knownAccount {
		hashToCompare = *user.HashedPassword
	}

	passwordErr := bcrypt.CompareHashAndPassword([]byte(hashToCompare), []byte(request.Password))
	if !knownAccount || passwordErr != nil {
		if recordFailure() {
			return nil, ErrLoginRateLimited
		}
		return nil, errInvalidCredentials
	}

	// The password is correct from here on, so status-specific errors don't
	// leak anything new.
	if user.Status == users_enums.UserStatusInvited {
		if recordFailure() {
			return nil, ErrLoginRateLimited
		}
		return nil, errors.New("user account is not passed sign up yet")
	}

	if user.Status != users_enums.UserStatusActive {
		if recordFailure() {
			return nil, ErrLoginRateLimited
		}
		return nil, errors.New("user account is deactivated")
	}

	// A correct password resets both counters, so a shared IP isn't punished for
	// earlier bad guesses.
	if err := resetLoginFailures(emailKey); err != nil {
		logger.GetLogger().Error("failed to reset login failure count", "error", err)
	}
	if clientIP != "" {
		if err := resetLoginFailures("ip:" + clientIP); err != nil {
			logger.GetLogger().Error("failed to reset login failure count", "error", err)
		}
	}

	outcome, err := s.completeAuthentication(user)
	if err != nil {
		return nil, err
	}

	if outcome.Token != "" {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User signed in with email: %s", user.Email),
			&user.ID,
			nil,
		)
	}

	return outcome.toSignInResponse(), nil
}

// authOutcome is either an access token (MFA satisfied) or a short-lived MFA
// challenge token for the frontend.
type authOutcome struct {
	Token            string
	MFARequired      bool
	MFASetupRequired bool
	MFAToken         string
	UserID           uuid.UUID
	Email            string
}

func (o *authOutcome) toSignInResponse() *users_dto.SignInResponseDTO {
	response := &users_dto.SignInResponseDTO{
		MFARequired:      o.MFARequired,
		MFASetupRequired: o.MFASetupRequired,
		MFAToken:         o.MFAToken,
	}
	if o.Token != "" {
		response.UserID = o.UserID
		response.Email = o.Email
		response.Token = o.Token
	}
	return response
}

func (o *authOutcome) toOAuthCallbackResponse(isNewUser bool) *users_dto.OAuthCallbackResponseDTO {
	response := &users_dto.OAuthCallbackResponseDTO{
		MFARequired:      o.MFARequired,
		MFASetupRequired: o.MFASetupRequired,
		MFAToken:         o.MFAToken,
	}
	if o.Token != "" {
		response.UserID = o.UserID
		response.Email = o.Email
		response.Token = o.Token
		response.IsNewUser = isNewUser
	}
	return response
}

// completeAuthentication is the single point every login path goes through
// once identity is confirmed, so MFA enforcement applies uniformly:
//   - MFA enabled: issue a login challenge (/users/mfa/login-verify).
//   - MFA required but not enabled: issue a setup challenge (/users/mfa/login-setup).
//   - Neither: issue the access token.
func (s *UserService) completeAuthentication(user *users_models.User) (*authOutcome, error) {
	if user.MFAEnabled {
		token, err := mfa_services.GetMFAService().IssueLoginChallengeToken(user.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to issue MFA challenge: %w", err)
		}
		return &authOutcome{MFARequired: true, MFAToken: token}, nil
	}

	if user.IsMFARequired() {
		token, err := mfa_services.GetMFAService().IssueSetupChallengeToken(user.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to issue MFA setup challenge: %w", err)
		}
		return &authOutcome{MFASetupRequired: true, MFAToken: token}, nil
	}

	tokenResponse, err := s.GenerateAccessToken(user)
	if err != nil {
		return nil, err
	}

	return &authOutcome{
		Token:  tokenResponse.Token,
		UserID: tokenResponse.UserID,
		Email:  user.Email,
	}, nil
}

// GetUserFromToken validates a local access token and loads its user; every
// authenticated request goes through it.
func (s *UserService) GetUserFromToken(token string) (*users_models.User, error) {
	user, _, err := s.GetUserAndClaimsFromToken(token)
	return user, err
}

// jwtParseOptions: HS256 only, and v4's checks kept on v5 (exp and nbf are
// v5's defaults; iat in the future is rejected as v4 did).
var jwtParseOptions = []jwt.ParserOption{jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuedAt()}

// GetUserAndClaimsFromToken is GetUserFromToken plus the verified claims, which
// the middleware needs to tell exchanged and IdP-MFA sessions from local ones.
func (s *UserService) GetUserAndClaimsFromToken(token string) (*users_models.User, jwt.MapClaims, error) {
	secretKey, err := s.secretKeyRepository.GetSecretKey()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to get secret key: %w", ErrAuthUnavailable, err)
	}

	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	}, jwtParseOptions...)

	if err != nil {
		return nil, nil, fmt.Errorf("invalid token: %w", err)
	}

	if claims, ok := parsedToken.Claims.(jwt.MapClaims); ok && parsedToken.Valid {
		userIDStr, ok := claims["sub"].(string)
		if !ok {
			return nil, nil, errors.New("invalid token claims")
		}

		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			return nil, nil, errors.New("invalid token claims")
		}

		// Tokens issued before jti existed have nothing to revoke; they expire naturally.
		if jti, _ := claims["jti"].(string); jti != "" {
			revoked, err := checkTokenRevoked(jti)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %w", ErrAuthUnavailable, err)
			}
			if revoked {
				return nil, nil, errors.New("token has been revoked, please sign in again")
			}
		}

		user, err := s.userRepository.GetUserByIDWithoutPlan(userID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("user not found")
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: failed to get user: %w", ErrAuthUnavailable, err)
		}

		if !user.IsActiveUser() {
			return nil, nil, errors.New("user account is deactivated")
		}

		if passwordCreationTimeUnix, ok := claims["passwordCreationTime"].(float64); ok {
			tokenPasswordTime := time.Unix(int64(passwordCreationTimeUnix), 0)

			tokenTimeSeconds := tokenPasswordTime.Truncate(time.Second)
			userTimeSeconds := user.PasswordCreationTime.Truncate(time.Second)

			if !tokenTimeSeconds.Equal(userTimeSeconds) {
				return nil, nil, errors.New("password has been changed, please sign in again")
			}
		} else {
			return nil, nil, errors.New("invalid token claims: missing password creation time")
		}

		return user, claims, nil
	}

	return nil, nil, errors.New("invalid token")
}

// SignOut revokes the presented token. An already-invalid token, or one with no
// jti, is a no-op success: it already can't be used.
func (s *UserService) SignOut(token string) error {
	secretKey, err := s.secretKeyRepository.GetSecretKey()
	if err != nil {
		return fmt.Errorf("failed to get secret key: %w", err)
	}

	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	}, jwtParseOptions...)
	if err != nil {
		return nil //nolint:nilerr // see doc comment: an already-invalid token needs no revoking
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		return nil
	}

	jti, _ := claims["jti"].(string)
	if jti == "" {
		return nil
	}

	var ttl time.Duration
	if expUnix, ok := claims["exp"].(float64); ok {
		ttl = time.Until(time.Unix(int64(expUnix), 0))
	}

	if err := revokeToken(jti, ttl); err != nil {
		return err
	}

	if userIDStr, ok := claims["sub"].(string); ok {
		if userID, err := uuid.Parse(userIDStr); err == nil {
			notifySessionRevocation(userID)
		}
	}

	return nil
}

// accessTokenTTL bounds how long a leaked token stays usable; SignOut revokes
// one token immediately.
const accessTokenTTL = 30 * 24 * time.Hour

// AccessTokenOptions parameterizes GenerateAccessTokenWithOptions; the zero value
// is a standard local token. The multi-site exchange sets a shorter TTL and claims.
type AccessTokenOptions struct {
	// TTL overrides accessTokenTTL when > 0.
	TTL time.Duration
	// ExtraClaims can't override the reserved claims; those are set last.
	ExtraClaims map[string]any
	// NotASignIn marks a token minted for internal use (MCP executor calls), which
	// mustn't count as the user's login.
	NotASignIn bool
}

// AuthOriginClaim marks how a session was established: control-plane exchange
// (MFA enforced centrally), SSO with IdP-enforced MFA, or absent for a local login.
const (
	AuthOriginClaim        = "auth_origin"
	AuthOriginControlPlane = "control-plane"
	AuthOriginSSOIdPMFA    = "sso-idp-mfa"
	// AuthOriginAccessToken is an internal session for one MCP call made with a
	// token whose creating session passed MFA.
	AuthOriginAccessToken = "access-token"
)

// ClaimsSatisfyMFA reports whether a session's second factor was verified
// elsewhere; a control-plane session counts only if the user passed MFA there.
func ClaimsSatisfyMFA(claims map[string]any) bool {
	switch origin, _ := claims[AuthOriginClaim].(string); origin {
	case AuthOriginSSOIdPMFA, AuthOriginAccessToken:
		return true
	case AuthOriginControlPlane:
		return amrHasMFA(claims["amr"])
	default:
		return false
	}
}

func amrHasMFA(value any) bool {
	switch values := value.(type) {
	case []string:
		return slices.Contains(values, "mfa")
	case []any:
		return slices.Contains(values, any("mfa"))
	default:
		return false
	}
}

// oauthTokenOptions marks a session whose provider enforced MFA.
func oauthTokenOptions(idpMFATrusted bool) AccessTokenOptions {
	if !idpMFATrusted {
		return AccessTokenOptions{}
	}
	return AccessTokenOptions{ExtraClaims: map[string]any{AuthOriginClaim: AuthOriginSSOIdPMFA}}
}

// completeOAuthAuthentication skips local TOTP when the connector trusts the
// provider's MFA; a password sign-in to the same account still needs local MFA.
func (s *UserService) completeOAuthAuthentication(
	user *users_models.User,
	idpMFATrusted bool,
) (*authOutcome, error) {
	if !idpMFATrusted {
		return s.completeAuthentication(user)
	}

	tokenResponse, err := s.GenerateAccessTokenWithOptions(user, oauthTokenOptions(true))
	if err != nil {
		return nil, err
	}

	return &authOutcome{
		Token:  tokenResponse.Token,
		UserID: tokenResponse.UserID,
		Email:  user.Email,
	}, nil
}

// GenerateAccessToken mints a standard local access token.
func (s *UserService) GenerateAccessToken(user *users_models.User) (*users_dto.SignInResponseDTO, error) {
	return s.GenerateAccessTokenWithOptions(user, AccessTokenOptions{})
}

func (s *UserService) GenerateAccessTokenWithOptions(
	user *users_models.User,
	opts AccessTokenOptions,
) (*users_dto.SignInResponseDTO, error) {
	secretKey, err := s.secretKeyRepository.GetSecretKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get secret key: %w", err)
	}

	ttl := accessTokenTTL
	if opts.TTL > 0 {
		ttl = opts.TTL
	}
	now := time.Now().UTC()
	expiration := now.Add(ttl)

	claims := jwt.MapClaims{}
	for k, v := range opts.ExtraClaims {
		claims[k] = v
	}
	// Reserved claims last, so an ExtraClaims entry can never shadow them.
	claims["sub"] = user.ID.String()
	claims["exp"] = expiration.Unix()
	claims["iat"] = now.Unix()
	// A random jti lets SignOut revoke this one token without ending other sessions.
	claims["jti"] = uuid.NewString()
	claims["role"] = string(user.Role)
	claims["passwordCreationTime"] = user.PasswordCreationTime.Unix()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	if !opts.NotASignIn {
		// Every sign-in method ends here; failing to record it must not fail
		// the sign-in.
		if err := s.userRepository.SetLastLogin(user.ID, now); err != nil {
			logger.GetLogger().Warn("Failed to record the user's sign-in time", "user", user.ID, "error", err)
		}
	}

	return &users_dto.SignInResponseDTO{
		UserID: user.ID,
		Token:  tokenString,
	}, nil
}

// CreateInitialAdmin creates the password-less root admin if missing (email per
// config.EffectiveRootAdminEmail) and logs a one-time bootstrap token.
// Setup requires that token, so only someone with log access can claim the instance.
func (s *UserService) CreateInitialAdmin() error {
	email := config.GetEnv().EffectiveRootAdminEmail()

	rawToken, tokenHash, err := generateBootstrapToken()
	if err != nil {
		return fmt.Errorf("failed to generate bootstrap token: %w", err)
	}

	created, err := s.userRepository.CreateInitialAdmin(email, tokenHash)
	if err != nil {
		return err
	}

	if created {
		logger.GetLogger().Warn(
			"Root admin account created - bootstrap token required to set its password",
			"email", email,
			"bootstrapToken", rawToken,
		)
	}

	return nil
}

// SyncRootAdminEmail renames the root admin to config.EffectiveRootAdminEmail at
// startup when it differs; it fails if that email belongs to another account.
func (s *UserService) SyncRootAdminEmail() error {
	email := config.GetEnv().EffectiveRootAdminEmail()

	changed, err := s.userRepository.SyncRootAdminEmail(email)
	if err != nil {
		return err
	}

	if changed && s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("Root admin email updated to %s (ADMIN_EMAIL / role default)", email),
			nil,
			nil,
		)
	}

	return nil
}

func (s *UserService) IsRootAdminHasPassword() (bool, error) {
	admin, err := s.userRepository.GetRootAdmin()

	if err != nil {
		return false, fmt.Errorf("failed to get admin user: %w", err)
	}

	if admin == nil {
		return false, errors.New("admin user does not exist")
	}

	return admin.HasPassword(), nil
}

// SetRootAdminPassword completes first-run setup. It's unauthenticated, so the
// bootstrap token proves the caller is the operator.
func (s *UserService) SetRootAdminPassword(password, bootstrapToken string) error {
	admin, err := s.userRepository.GetRootAdmin()
	if err != nil {
		return fmt.Errorf("failed to get admin user: %w", err)
	}

	if admin == nil {
		return errors.New("admin user does not exist")
	}

	if admin.HasPassword() {
		return errors.New("admin password is already set")
	}

	// A nil hash means the account predates bootstrap tokens; allow setup rather
	// than lock it out.
	if admin.BootstrapTokenHash != nil &&
		subtle.ConstantTimeCompare(
			[]byte(hashBootstrapToken(bootstrapToken)), []byte(*admin.BootstrapTokenHash),
		) != 1 {
		return errors.New(
			"invalid bootstrap token - check the server logs for the token printed when the admin account was created",
		)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	if err := s.userRepository.UpdateUserPassword(admin.ID, string(hashedPassword)); err != nil {
		return fmt.Errorf("failed to set admin password: %w", err)
	}

	if err := s.userRepository.ClearBootstrapToken(admin.ID); err != nil {
		return fmt.Errorf("failed to clear bootstrap token: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			"Admin password set",
			&admin.ID,
			nil,
		)
	}

	return nil
}

// ErrCurrentPasswordIncorrect is returned by ChangeUserPassword.
var ErrCurrentPasswordIncorrect = errors.New("current password is incorrect")

// ChangeUserPasswordByEmail skips the current-password check; its only caller
// is the CLI reset flow (operator with shell access).
func (s *UserService) ChangeUserPasswordByEmail(email string, newPassword string) error {
	user, err := s.userRepository.GetUserByEmail(email)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}
	// GetUserByEmail returns (nil, nil) for no such user.
	if user == nil {
		return fmt.Errorf("no user found with email %q", email)
	}

	return s.updatePassword(user.ID, newPassword, "Password changed")
}

// ResetMFAByEmail is the CLI MFA reset for a lost authenticator when no other
// admin can reset it. It reports whether MFA had been enabled.
func (s *UserService) ResetMFAByEmail(email string) (wasEnabled bool, err error) {
	user, err := s.userRepository.GetUserByEmail(email)
	if err != nil {
		return false, fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return false, fmt.Errorf("no user found with email %q", email)
	}

	if err := s.userRepository.ClearMFA(user.ID); err != nil {
		return false, fmt.Errorf("failed to reset MFA: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("MFA reset for %s from the command line", user.Email),
			&user.ID,
			nil,
		)
	}

	return user.MFAEnabled, nil
}

// ErrRootAdminAlreadySetUp: a bootstrap token would grant nothing once the root
// admin has a password.
var ErrRootAdminAlreadySetUp = errors.New("the root admin already has a password; reset it with --email instead")

// ResetBootstrapToken issues a new bootstrap token (CLI), since the original is
// logged only once. Only while the root admin has no password.
func (s *UserService) ResetBootstrapToken() (rawToken string, err error) {
	admin, err := s.userRepository.GetRootAdmin()
	if err != nil {
		return "", fmt.Errorf("failed to get the root admin: %w", err)
	}
	if admin == nil {
		return "", errors.New("there is no root admin yet; start the server once to create it")
	}
	if admin.HashedPassword != nil {
		return "", ErrRootAdminAlreadySetUp
	}

	rawToken, tokenHash, err := generateBootstrapToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate bootstrap token: %w", err)
	}
	if err := s.userRepository.SetBootstrapTokenHash(admin.ID, tokenHash); err != nil {
		return "", fmt.Errorf("failed to store the bootstrap token: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("Root admin bootstrap token reset from the command line", &admin.ID, nil)
	}

	return rawToken, nil
}

// Duplicated from api_keys rather than imported, to avoid a feature-to-feature
// dependency for two error values.
var (
	ErrMFACodeRequired = errors.New("MFA code required to confirm this action")
	ErrMFACodeInvalid  = errors.New("invalid MFA code")
)

// requireMFAIfEnabled is the step-up check, enforced only when MFA is enabled.
func (s *UserService) requireMFAIfEnabled(user *users_models.User, code *string) error {
	if !user.MFAEnabled {
		return nil
	}
	if code == nil || strings.TrimSpace(*code) == "" {
		return ErrMFACodeRequired
	}
	if err := mfa_services.GetMFAService().VerifyCurrentUserCode(user, strings.TrimSpace(*code)); err != nil {
		if errors.Is(err, mfa_services.ErrTooManyAttempts) {
			return err
		}
		return ErrMFACodeInvalid
	}
	return nil
}

// ChangeUserPassword requires the current password, and an MFA code when MFA is
// enabled, so a stolen token or password alone can't take over the account.
func (s *UserService) ChangeUserPassword(userID uuid.UUID, currentPassword, newPassword string, mfaCode *string) error {
	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if !user.HasPassword() {
		return errors.New("user has no password set")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*user.HashedPassword), []byte(currentPassword)); err != nil {
		return ErrCurrentPasswordIncorrect
	}

	if err := s.requireMFAIfEnabled(user, mfaCode); err != nil {
		return err
	}

	return s.updatePassword(userID, newPassword, "Password changed")
}

func (s *UserService) updatePassword(userID uuid.UUID, newPassword, auditMessage string) error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	if err := s.userRepository.UpdateUserPassword(userID, string(hashedPassword)); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	s.auditLogWriter.WriteAuditLog(auditMessage, &userID, nil)

	return nil
}

// InviteUser handles the global /users/invite endpoint. IntendedProjectID is
// unverified, so the audit entry is never attributed to a project.
func (s *UserService) InviteUser(
	request *users_dto.InviteUserRequestDTO,
	invitedBy *users_models.User,
) (*users_dto.InviteUserResponseDTO, error) {
	return s.inviteUser(request, invitedBy, nil)
}

// InviteUserForProject is InviteUser for a caller that has already verified
// access to validatedProjectID, which the audit entry is attributed to.
func (s *UserService) InviteUserForProject(
	request *users_dto.InviteUserRequestDTO,
	invitedBy *users_models.User,
	validatedProjectID uuid.UUID,
) (*users_dto.InviteUserResponseDTO, error) {
	return s.inviteUser(request, invitedBy, &validatedProjectID)
}

func (s *UserService) inviteUser(
	request *users_dto.InviteUserRequestDTO,
	invitedBy *users_models.User,
	auditProjectID *uuid.UUID,
) (*users_dto.InviteUserResponseDTO, error) {
	settings, err := s.settingsService.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	if !invitedBy.CanInviteUsers(settings) {
		return nil, errors.New("insufficient permissions to invite users")
	}

	existingUser, err := s.userRepository.GetUserByEmail(request.Email)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing user: %w", err)
	}
	if existingUser != nil {
		return nil, errors.New("user with this email already exists")
	}

	basicPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
	if err != nil {
		return nil, fmt.Errorf("failed to get default plan: %w", err)
	}

	var planID *uuid.UUID
	if basicPlan != nil {
		planID = &basicPlan.ID
	}

	// Only the hash is stored; the raw token is returned once.
	rawInviteToken, inviteTokenHash, err := generateInviteToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate invite token: %w", err)
	}
	inviteTokenExpiresAt := time.Now().UTC().Add(inviteTokenTTL)

	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                request.Email,
		Name:                 "User",
		HashedPassword:       nil,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		PlanID:               planID,
		Status:               users_enums.UserStatusInvited,
		CreatedAt:            time.Now().UTC(),
		InviteTokenHash:      &inviteTokenHash,
		InviteTokenExpiresAt: &inviteTokenExpiresAt,
	}

	if err := s.userRepository.CreateUser(user); err != nil {
		return nil, fmt.Errorf("failed to create invited user: %w", err)
	}

	message := fmt.Sprintf("User invited: %s", request.Email)
	if auditProjectID != nil {
		message += fmt.Sprintf(" for project %s", auditProjectID.String())
	}
	s.auditLogWriter.WriteAuditLog(
		message,
		&invitedBy.ID,
		auditProjectID,
	)

	return &users_dto.InviteUserResponseDTO{
		ID:                  user.ID,
		Email:               user.Email,
		IntendedProjectID:   request.IntendedProjectID,
		IntendedProjectRole: request.IntendedProjectRole,
		CreatedAt:           user.CreatedAt,
		InviteToken:         rawInviteToken,
	}, nil
}

func (s *UserService) GetUserByID(userID uuid.UUID) (*users_models.User, error) {
	return s.userRepository.GetUserByID(userID)
}

func (s *UserService) GetUserByEmail(email string) (*users_models.User, error) {
	return s.userRepository.GetUserByEmail(email)
}

func (s *UserService) GetCurrentUserProfile(user *users_models.User) *users_dto.UserProfileResponseDTO {
	return &users_dto.UserProfileResponseDTO{
		ID:          user.ID,
		Email:       user.Email,
		Name:        user.Name,
		Role:        user.Role,
		IsActive:    user.IsActiveUser(),
		CreatedAt:   user.CreatedAt,
		IsRootAdmin: user.IsRootAdmin,

		LastLoginAt:  user.LastLoginAt,
		LastActiveAt: user.LastActiveAt,
		MFAEnabled:   user.MFAEnabled,
	}
}

// UpdateUserInfo changes the caller's name and/or email. An email change needs
// the current password (a stolen token alone mustn't redirect account email);
// password-less accounts can't change it here. The new address isn't verified yet.
func (s *UserService) UpdateUserInfo(
	userID uuid.UUID,
	request *users_dto.UpdateUserInfoRequestDTO,
) error {
	user, err := s.userRepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	emailChanging := request.Email != nil && *request.Email != user.Email

	if user.IsRootAdmin && emailChanging {
		return errors.New("admin email cannot be changed here - set ADMIN_EMAIL in .env instead")
	}

	if emailChanging {
		if !user.HasPassword() {
			return errors.New(
				"email cannot be self-service changed on an account with no password set - contact an administrator",
			)
		}
		if err := bcrypt.CompareHashAndPassword(
			[]byte(*user.HashedPassword), []byte(request.CurrentPassword),
		); err != nil {
			return ErrCurrentPasswordIncorrect
		}
		if err := s.requireMFAIfEnabled(user, request.MFACode); err != nil {
			return err
		}

		existingUser, err := s.userRepository.GetUserByEmail(*request.Email)
		if err != nil {
			return fmt.Errorf("failed to check email: %w", err)
		}
		if existingUser != nil {
			return errors.New("email is already taken by another user")
		}
	}

	if err := s.userRepository.UpdateUserInfo(userID, request.Name, request.Email); err != nil {
		return fmt.Errorf("failed to update user info: %w", err)
	}

	s.auditLogWriter.WriteAuditLog("User info updated", &userID, nil)
	return nil
}

func (s *UserService) OnBeforePlanDeletion(planID uuid.UUID) error {
	usersWithPlanCount, err := s.userRepository.CountUsersByPlan(planID)
	if err != nil {
		return fmt.Errorf("failed to count users with plan: %w", err)
	}

	if usersWithPlanCount > 0 {
		return errors.New("cannot delete plan with assigned users")
	}

	return nil
}

// errUnsupportedOAuthProvider stops GenerateOAuthState minting a state token
// the callback would never accept.
var errUnsupportedOAuthProvider = errors.New("unsupported oauth provider")

// GenerateOAuthState mints a signed CSRF state token for the GitHub/Google
// authorize URL (see oauth_state.go).
func (s *UserService) GenerateOAuthState(provider string) (string, error) {
	if provider != "github" && provider != "google" {
		return "", errUnsupportedOAuthProvider
	}
	return signOAuthState(s.secretKeyRepository, provider)
}

func (s *UserService) HandleGitHubOAuth(code, redirectUri, state string) (*users_dto.OAuthCallbackResponseDTO, error) {
	if err := verifyOAuthState(s.secretKeyRepository, state, "github"); err != nil {
		return nil, err
	}
	return s.handleGitHubOAuthWithEndpoint(code, redirectUri, github.Endpoint, "https://api.github.com/user")
}

func (s *UserService) handleGitHubOAuthWithEndpoint(
	code, redirectUri string,
	endpoint oauth2.Endpoint,
	userAPIURL string,
) (*users_dto.OAuthCallbackResponseDTO, error) {
	env := config.GetEnv()

	oauthConfig := &oauth2.Config{
		ClientID:     env.GitHubClientID,
		ClientSecret: env.GitHubClientSecret,
		RedirectURL:  redirectUri,
		Endpoint:     endpoint,
		Scopes:       []string{"user:email"},
	}

	token, err := oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	client := oauthConfig.Client(context.Background(), token)
	resp, err := client.Get(userAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var githubUser struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}

	if err := json.Unmarshal(body, &githubUser); err != nil {
		return nil, fmt.Errorf("failed to parse user info: %w", err)
	}

	// /user's email isn't guaranteed verified, and verification gates account
	// linking, so always resolve via /user/emails.
	email, emailVerified, err := s.fetchGitHubEmail(client, userAPIURL, githubUser.Email)
	if err != nil {
		return nil, err
	}

	name := githubUser.Name
	if name == "" {
		name = githubUser.Login
	}

	oauthID := fmt.Sprintf("%d", githubUser.ID)
	return s.getOrCreateUserFromOAuth(oauthID, email, name, "github", nil, emailVerified, nil, false, false)
}

func (s *UserService) HandleGoogleOAuth(code, redirectUri, state string) (*users_dto.OAuthCallbackResponseDTO, error) {
	if err := verifyOAuthState(s.secretKeyRepository, state, "google"); err != nil {
		return nil, err
	}
	return s.handleGoogleOAuthWithEndpoint(
		code,
		redirectUri,
		google.Endpoint,
		"https://www.googleapis.com/oauth2/v2/userinfo",
	)
}

func (s *UserService) handleGoogleOAuthWithEndpoint(
	code, redirectUri string,
	endpoint oauth2.Endpoint,
	userAPIURL string,
) (*users_dto.OAuthCallbackResponseDTO, error) {
	env := config.GetEnv()

	oauthConfig := &oauth2.Config{
		ClientID:     env.GoogleClientID,
		ClientSecret: env.GoogleClientSecret,
		RedirectURL:  redirectUri,
		Endpoint:     endpoint,
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	}

	token, err := oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	client := oauthConfig.Client(context.Background(), token)
	resp, err := client.Get(userAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var googleUser struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name"`
	}

	if err := json.Unmarshal(body, &googleUser); err != nil {
		return nil, fmt.Errorf("failed to parse user info: %w", err)
	}

	if googleUser.Email == "" {
		return nil, errors.New("google account has no email")
	}

	name := googleUser.Name
	if name == "" {
		name = "User"
	}

	return s.getOrCreateUserFromOAuth(
		googleUser.ID, googleUser.Email, name, "google", nil, googleUser.VerifiedEmail, nil, false, false,
	)
}

// HandleSSOCallback finishes a portal OIDC connector login; account
// find-or-create is shared with GitHub/Google via getOrCreateUserFromOAuth.
func (s *UserService) HandleSSOCallback(
	request *sso_dto.SSOCallbackRequestDTO,
	clientIP string,
) (*users_dto.OAuthCallbackResponseDTO, error) {
	// Each callback costs an outbound token call to the provider.
	if clientIP != "" {
		limited, err := countRequest("sso-callback-ip:"+clientIP, maxSSOCallbacksPerIP, ssoCallbackWindow)
		if err != nil {
			return nil, fmt.Errorf("failed to check sign-in rate limit: %w", err)
		}
		if limited {
			return nil, ErrLoginRateLimited
		}
	}

	claims, err := sso_services.GetConnectorService().HandleCallback(request)
	if err != nil {
		return nil, err
	}

	var onIdentified func(userID uuid.UUID)
	if claims.GroupsSyncEnabled && !claims.GroupsUnknown {
		onIdentified = func(userID uuid.UUID) { s.syncSSOGroups(userID, claims) }
	}

	emailTrusted := claims.EmailVerified || !claims.RequireVerifiedEmail

	return s.getOrCreateUserFromOAuth(
		claims.Subject, claims.Email, claims.Name, "sso", &claims.ConnectorID, emailTrusted, onIdentified,
		s.ssoGroupsGrantAccess(claims), claims.IdPMFAVerified,
	)
}

// ssoGroupsGrantAccess reports whether the user's IdP groups match a mapping.
// Such a user is created even with registration closed: an admin mapped the group on purpose.
func (s *UserService) ssoGroupsGrantAccess(claims *sso_services.IdentityClaims) bool {
	if !claims.GroupsSyncEnabled || claims.GroupsUnknown || len(claims.Groups) == 0 {
		return false
	}
	log := logger.GetLogger()

	grants, err := sso_services.GetConnectorService().ResolveGroupGrants(claims.ConnectorID, claims.Groups)
	if err != nil {
		log.Error("SSO sign-in: failed to resolve group mappings", "error", err)
		return false
	}
	if grants.GlobalAdmin || len(grants.ProjectRoles) > 0 {
		return true
	}

	if s.ssoSiteGrantSyncer != nil {
		mapped, err := s.ssoSiteGrantSyncer.GroupsGrantSiteAccess(claims.ConnectorID, claims.Groups)
		if err != nil {
			log.Error("SSO sign-in: failed to check site group mappings", "error", err)
			return false
		}
		return mapped
	}
	return false
}

// syncSSOGroups never blocks sign-in on failure: the next sign-in re-syncs.
func (s *UserService) syncSSOGroups(userID uuid.UUID, claims *sso_services.IdentityClaims) {
	log := logger.GetLogger()

	grants, err := sso_services.GetConnectorService().ResolveGroupGrants(claims.ConnectorID, claims.Groups)
	if err != nil {
		log.Error("SSO group sync: failed to resolve group mappings", "userId", userID, "error", err)
		return
	}

	if s.groupMembershipSyncer != nil {
		if err := s.groupMembershipSyncer.SyncSSOGroupMemberships(
			userID, claims.ConnectorID, grants.ProjectRoles,
		); err != nil {
			log.Error("SSO group sync failed", "userId", userID, "error", err)
		}
	}

	s.syncSSOGlobalAdmin(userID, claims.ConnectorID, grants.GlobalAdmin)

	// Control plane: groups also decide Site access (sso_site_group_mappings).
	if s.ssoSiteGrantSyncer != nil {
		if err := s.ssoSiteGrantSyncer.SyncSSOSiteGrants(userID, claims.ConnectorID, claims.Groups); err != nil {
			log.Error("SSO site grant sync failed", "userId", userID, "error", err)
		}
	}
}

// syncSSOGlobalAdmin grants or revokes global Admin per groups; see
// UserRepository.GrantSSOAdmin/RevokeSSOAdmin for what is never touched.
func (s *UserService) syncSSOGlobalAdmin(userID, connectorID uuid.UUID, shouldBeAdmin bool) {
	log := logger.GetLogger()

	var changed bool
	var err error
	action := "promoted to Admin by SSO group"
	if shouldBeAdmin {
		changed, err = s.userRepository.GrantSSOAdmin(userID, connectorID)
	} else {
		action = "demoted from Admin (no longer in an SSO admin group)"
		changed, err = s.userRepository.RevokeSSOAdmin(userID, connectorID)
	}
	if err != nil {
		log.Error("SSO global admin sync failed", "userId", userID, "error", err)
		return
	}

	if changed && s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("SSO group sync: user "+action, &userID, nil)
	}
}

// getOrCreateUserFromOAuth is shared by every OAuth/OIDC login. ssoConnectorID
// is nil for GitHub/Google (users table columns) and set for portal connectors
// (user_sso_identities).
func (s *UserService) getOrCreateUserFromOAuth(
	oauthID, email, name, provider string,
	ssoConnectorID *uuid.UUID,
	emailTrusted bool,
	onUserIdentified func(userID uuid.UUID),
	// groupsGrantAccess lets a mapped SSO user be created without open registration.
	groupsGrantAccess bool,
	// idpMFATrusted: the provider enforces MFA (see completeOAuthAuthentication).
	idpMFATrusted bool,
) (*users_dto.OAuthCallbackResponseDTO, error) {
	var existingUser *users_models.User
	var err error

	switch {
	case ssoConnectorID != nil:
		existingUser, err = s.ssoIdentityRepository.GetUserByIdentity(*ssoConnectorID, oauthID)
	case provider == "github":
		existingUser, err = s.userRepository.GetUserByGitHubOAuthID(oauthID)
	default:
		existingUser, err = s.userRepository.GetUserByGoogleOAuthID(oauthID)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to check OAuth ID: %w", err)
	}

	if existingUser != nil {
		// No token (it would be refused anyway) and no "signed in" audit.
		if !existingUser.IsActiveUser() {
			return nil, errOAuthAccountDeactivated
		}
		if onUserIdentified != nil {
			onUserIdentified(existingUser.ID)
			// The sync may have changed their role, which MFA below depends on.
			if refreshed, err := s.userRepository.GetUserByID(existingUser.ID); err == nil && refreshed != nil {
				existingUser = refreshed
			}
		}

		outcome, err := s.completeOAuthAuthentication(existingUser, idpMFATrusted)
		if err != nil {
			return nil, err
		}

		if outcome.Token != "" && s.auditLogWriter != nil {
			s.auditLogWriter.WriteAuditLog(
				fmt.Sprintf("User signed in via %s", provider),
				&existingUser.ID,
				nil,
			)
		}

		return outcome.toOAuthCallbackResponse(false), nil
	}

	userByEmail, err := s.userRepository.GetUserByEmail(email)
	if err != nil {
		return nil, fmt.Errorf("failed to check email: %w", err)
	}

	if !emailTrusted {
		// Linking by an unverified email would let whoever controls that claim take
		// over the account.
		return nil, ErrSSOEmailNotVerified
	}

	if userByEmail != nil {
		// Auto-linking by email to an account with its own sign-in method (or an admin)
		// would hand the IdP's email owner that account; refuse instead.
		// Invited password-less Member accounts are unaffected.
		if userByEmail.Role == users_enums.UserRoleAdmin || userByEmail.IsRootAdmin {
			return nil, ErrOAuthAccountNotLinkable
		}
		if userByEmail.HasPassword() || userByEmail.GitHubOAuthID != nil || userByEmail.GoogleOAuthID != nil {
			return nil, ErrOAuthAccountNotLinkable
		}
		if !userByEmail.IsActiveUser() && userByEmail.Status != users_enums.UserStatusInvited {
			return nil, errOAuthAccountDeactivated
		}

		if userByEmail.Status == users_enums.UserStatusInvited {
			if err := s.userRepository.UpdateUserStatus(userByEmail.ID, users_enums.UserStatusActive); err != nil {
				return nil, fmt.Errorf("failed to activate user: %w", err)
			}

			if err := s.userRepository.UpdateUserInfo(userByEmail.ID, &name, nil); err != nil {
				return nil, fmt.Errorf("failed to update name: %w", err)
			}
		}

		if ssoConnectorID != nil {
			if err := s.ssoIdentityRepository.LinkIdentity(userByEmail.ID, *ssoConnectorID, oauthID); err != nil {
				return nil, fmt.Errorf("failed to link SSO identity: %w", err)
			}
		} else {
			oauthColumn := "github_oauth_id"
			if provider == "google" {
				oauthColumn = "google_oauth_id"
			}

			if err := s.userRepository.LinkOAuthID(userByEmail.ID, oauthColumn, oauthID); err != nil {
				return nil, fmt.Errorf("failed to link OAuth ID: %w", err)
			}
		}

		if onUserIdentified != nil {
			onUserIdentified(userByEmail.ID)
		}

		user, err := s.userRepository.GetUserByID(userByEmail.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get updated user: %w", err)
		}

		outcome, err := s.completeOAuthAuthentication(user, idpMFATrusted)
		if err != nil {
			return nil, err
		}

		if outcome.Token != "" && s.auditLogWriter != nil {
			s.auditLogWriter.WriteAuditLog(
				fmt.Sprintf("%s OAuth linked to existing account", provider),
				&user.ID,
				nil,
			)
		}

		return outcome.toOAuthCallbackResponse(false), nil
	}

	settings, err := s.settingsService.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	if !settings.IsAllowExternalRegistrations && !groupsGrantAccess {
		if ssoConnectorID != nil {
			return nil, ErrSSONoMappedGroup
		}
		return nil, errors.New("external registration is disabled")
	}

	basicPlan, err := s.userPlanRepository.GetPlanByType(users_enums.UserPlanTypeDefault)
	if err != nil {
		return nil, fmt.Errorf("failed to get default plan: %w", err)
	}

	var planID *uuid.UUID
	if basicPlan != nil {
		planID = &basicPlan.ID
	}

	var githubOAuthID *string
	var googleOAuthID *string
	if ssoConnectorID == nil {
		if provider == "github" {
			githubOAuthID = &oauthID
		} else {
			googleOAuthID = &oauthID
		}
	}

	newUser := &users_models.User{
		ID:                   uuid.New(),
		Email:                email,
		Name:                 name,
		HashedPassword:       nil,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		PlanID:               planID,
		Status:               users_enums.UserStatusActive,
		GitHubOAuthID:        githubOAuthID,
		GoogleOAuthID:        googleOAuthID,
		CreatedAt:            time.Now().UTC(),
	}

	if err := s.userRepository.CreateUser(newUser); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	if ssoConnectorID != nil {
		if err := s.ssoIdentityRepository.LinkIdentity(newUser.ID, *ssoConnectorID, oauthID); err != nil {
			return nil, fmt.Errorf("failed to link SSO identity: %w", err)
		}
	}

	tokenUser := newUser
	if onUserIdentified != nil {
		onUserIdentified(newUser.ID)

		// A group may have just made them an Admin, who must complete MFA
		// before getting a token - unless the provider enforces MFA.
		if refreshed, err := s.userRepository.GetUserByID(newUser.ID); err == nil && refreshed != nil {
			tokenUser = refreshed
			if refreshed.IsMFARequired() && !idpMFATrusted {
				outcome, err := s.completeAuthentication(refreshed)
				if err != nil {
					return nil, err
				}
				return outcome.toOAuthCallbackResponse(true), nil
			}
		}
	}

	tokenResponse, err := s.GenerateAccessTokenWithOptions(tokenUser, oauthTokenOptions(idpMFATrusted))
	if err != nil {
		return nil, err
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("User registered via %s OAuth: %s", provider, email),
			&newUser.ID,
			nil,
		)
	}

	return &users_dto.OAuthCallbackResponseDTO{
		UserID:    tokenResponse.UserID,
		Email:     newUser.Email,
		Token:     tokenResponse.Token,
		IsNewUser: true,
	}, nil
}

// fetchGitHubEmail prefers /user/emails (the only source of verified status);
// the fallback /user email is reported unverified.
func (s *UserService) fetchGitHubEmail(
	client *http.Client,
	userAPIURL, fallbackEmail string,
) (email string, verified bool, err error) {
	emailsURL := "https://api.github.com/user/emails"
	if userAPIURL != "https://api.github.com/user" {
		baseURL := userAPIURL[:len(userAPIURL)-len("/user")]
		emailsURL = baseURL + "/user/emails"
	}

	resp, err := client.Get(emailsURL)
	if err != nil {
		return "", false, fmt.Errorf("failed to get user emails: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", false, fmt.Errorf("failed to read emails response: %w", err)
		}
		if err := json.Unmarshal(body, &emails); err != nil {
			return "", false, fmt.Errorf("failed to parse emails: %w", err)
		}
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, true, nil
		}
	}
	for _, e := range emails {
		if e.Verified {
			return e.Email, true, nil
		}
	}
	if len(emails) > 0 {
		return emails[0].Email, false, nil
	}
	if fallbackEmail != "" {
		return fallbackEmail, false, nil
	}

	return "", false, errors.New("github account has no accessible email")
}
