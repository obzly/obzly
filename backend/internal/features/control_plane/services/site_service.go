package control_plane_services

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"obzly/internal/config"
	control_plane_dto "obzly/internal/features/control_plane/dto"
	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_repositories "obzly/internal/features/control_plane/repositories"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/egress"
)

var (
	ErrSiteNotFound     = errors.New("site not found")
	ErrSiteIDExists     = errors.New("a site with this id already exists")
	ErrSiteForbidden    = errors.New("only an administrator can manage sites")
	ErrSiteInvalidID    = errors.New("site id must match ^[a-z0-9][a-z0-9-]{0,62}$")
	ErrSiteInvalidAPURL = errors.New("api url is invalid")

	ErrSiteInvalidDisplayName = fmt.Errorf("friendly name must be at most %d characters", maxSiteDisplayNameLength)
	ErrSiteInvalidEnvironment = errors.New("environment must be production, staging, development or empty")
)

const maxSiteDisplayNameLength = 64

var siteEnvironments = map[string]bool{"": true, "production": true, "staging": true, "development": true}

type SiteService struct {
	siteRepository *control_plane_repositories.SiteRepository
	auditLogWriter users_interfaces.AuditLogWriter
}

func (s *SiteService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

// siteIDPattern mirrors config.SiteIDPattern; the ID must also be a safe path segment
// for /sites/:siteId/*path.
var siteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateSiteURL enforces the SSRF floor: absolute, https:// unless
// SITE_ALLOW_INSECURE_URLS, host-only (anything else is a smuggling vector, since the
// proxy appends /api/v1/...), and never loopback/link-local/cloud metadata
// (egress.PrivateNetworkFloor, also inside NAT64/6to4). Private ranges are allowed.
func validateSiteURL(raw string, allowInsecure bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSiteInvalidAPURL, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && (!allowInsecure || scheme != "http") {
		return fmt.Errorf("%w: scheme must be https", ErrSiteInvalidAPURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: host is required", ErrSiteInvalidAPURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: userinfo is not allowed", ErrSiteInvalidAPURL)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%w: path is not allowed", ErrSiteInvalidAPURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: query/fragment are not allowed", ErrSiteInvalidAPURL)
	}

	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedSiteIP(ip) {
			return fmt.Errorf("%w: address is not allowed", ErrSiteInvalidAPURL)
		}
	}
	// Hostnames aren't resolved here: the pinned dialer enforces the policy on the
	// address actually dialed, which is what defeats rebinding.

	return nil
}

func isDisallowedSiteIP(ip net.IP) bool {
	return egress.PrivateNetworkFloor(ip) != nil
}

func (s *SiteService) CreateSite(user *users_models.User, request control_plane_dto.CreateSiteRequestDTO) (
	*control_plane_models.Site, error,
) {
	if !user.CanUpdateSettings() {
		return nil, ErrSiteForbidden
	}

	id := strings.TrimSpace(request.ID)
	if !siteIDPattern.MatchString(id) {
		return nil, ErrSiteInvalidID
	}

	apiURL := strings.TrimRight(strings.TrimSpace(request.ApiURL), "/")
	if err := validateSiteURL(apiURL, config.GetEnv().SiteAllowInsecureURLs); err != nil {
		return nil, err
	}

	displayName, err := normalizeSiteDisplayName(request.DisplayName)
	if err != nil {
		return nil, err
	}
	environment, err := normalizeSiteEnvironment(request.Environment)
	if err != nil {
		return nil, err
	}

	existing, err := s.siteRepository.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing site: %w", err)
	}
	if existing != nil {
		return nil, ErrSiteIDExists
	}

	site := &control_plane_models.Site{
		ID:          id,
		Name:        strings.TrimSpace(request.Name),
		ApiURL:      apiURL,
		Enabled:     true,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		DisplayName: displayName,
		Environment: environment,
	}
	if err := s.siteRepository.Create(site); err != nil {
		return nil, fmt.Errorf("failed to create site: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(fmt.Sprintf("Site registered: %s (%s)", site.ID, site.ApiURL), &user.ID, nil)
	}
	return site, nil
}

func (s *SiteService) UpdateSite(
	user *users_models.User, id string, request control_plane_dto.UpdateSiteRequestDTO,
) (*control_plane_models.Site, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrSiteForbidden
	}

	site, err := s.siteRepository.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to load site: %w", err)
	}
	if site == nil {
		return nil, ErrSiteNotFound
	}

	apiURL := strings.TrimRight(strings.TrimSpace(request.ApiURL), "/")
	if err := validateSiteURL(apiURL, config.GetEnv().SiteAllowInsecureURLs); err != nil {
		return nil, err
	}

	if request.DisplayName != nil {
		displayName, err := normalizeSiteDisplayName(*request.DisplayName)
		if err != nil {
			return nil, err
		}
		site.DisplayName = displayName
	}
	if request.Environment != nil {
		environment, err := normalizeSiteEnvironment(*request.Environment)
		if err != nil {
			return nil, err
		}
		site.Environment = environment
	}

	urlChanged := site.ApiURL != apiURL
	site.Name = strings.TrimSpace(request.Name)
	site.ApiURL = apiURL
	site.Enabled = request.Enabled
	site.UpdatedAt = time.Now().UTC()
	if urlChanged {
		// The pinned shared-config key stays: the new address must prove it holds it, or
		// an admin accepts its new one.
		site.SharedConfigKeyCheckedAt = nil
	}

	if err := s.siteRepository.Update(site); err != nil {
		return nil, fmt.Errorf("failed to update site: %w", err)
	}
	if urlChanged {
		siteTokenService.PurgeSite(site.ID)
	}

	if s.auditLogWriter != nil {
		message := fmt.Sprintf("Site updated: %s", site.ID)
		if urlChanged {
			message += fmt.Sprintf(" (address now %s)", site.ApiURL)
		}
		s.auditLogWriter.WriteAuditLog(message, &user.ID, nil)
	}
	return site, nil
}

func (s *SiteService) DeleteSite(user *users_models.User, id string) error {
	if !user.CanUpdateSettings() {
		return ErrSiteForbidden
	}
	if err := s.siteRepository.Delete(id); err != nil {
		return fmt.Errorf("failed to delete site: %w", err)
	}
	siteTokenService.PurgeSite(id)
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(fmt.Sprintf("Site removed: %s", id), &user.ID, nil)
	}
	return nil
}

func (s *SiteService) GetSite(id string) (*control_plane_models.Site, error) {
	return s.siteRepository.GetByID(id)
}

func (s *SiteService) ListSites() ([]*control_plane_models.Site, error) {
	return s.siteRepository.List()
}

func normalizeSiteDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len([]rune(name)) > maxSiteDisplayNameLength {
		return "", ErrSiteInvalidDisplayName
	}
	return name, nil
}

func normalizeSiteEnvironment(raw string) (string, error) {
	environment := strings.ToLower(strings.TrimSpace(raw))
	if !siteEnvironments[environment] {
		return "", ErrSiteInvalidEnvironment
	}
	return environment, nil
}
