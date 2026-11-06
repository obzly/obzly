package synthetics

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/features/access_tokens"
	users_interfaces "obzly/internal/features/users/interfaces"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/synthetics/spec"
)

var (
	ErrForbidden = errors.New("only admins can change synthetic monitoring")
	ErrNotFound  = errors.New("not found")
	// ErrConflict: the monitor changed since the editor loaded it.
	ErrConflict = errors.New("this monitor was changed by someone else; reload it to see their changes")
	ErrInUse    = errors.New("monitors still send results to this destination")
	// ErrCloudInUse: a cloud connection still uses the probe group or destination.
	ErrCloudInUse = errors.New("a cloud connection still uses it; change or delete the connection first")
)

// onlineWithin: a probe that polled this recently counts as online (it
// holds a task long-poll open, so a live one is seen every minute).
const (
	onlineWithin = 3 * time.Minute
	// memberLiveFor: a member that hasn't polled for this long has left; its
	// monitors go to the others (members poll at least every ~25 s).
	memberLiveFor = 60 * time.Second
	// tokenGrace: how long a group's old token works after a new one.
	tokenGrace = 24 * time.Hour
)

var locationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

type Service struct {
	repository     *repository
	secrets        *users_repositories.SecretKeyRepository
	auditLogWriter users_interfaces.AuditLogWriter
	now            func() time.Time
}

// Checks

// ListChecks is every monitor; whole definitions (headers and bodies can
// hold inline secrets) for admins, a summary for everyone else.
func (s *Service) ListChecks(user *users_models.User) ([]CheckView, error) {
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	views := make([]CheckView, 0, len(checks))
	for _, check := range checks {
		if !user.CanUpdateSettings() {
			views = append(views, s.summary(check, destinations))
			continue
		}
		view, err := s.view(check, destinations)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) GetCheck(user *users_models.User, id uuid.UUID) (*CheckView, error) {
	if user.CanUpdateSettings() {
		return s.checkView(id)
	}
	check, err := s.findCheck(id)
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	view := s.summary(check, destinations)
	return &view, nil
}

func (s *Service) checkView(id uuid.UUID) (*CheckView, error) {
	check, err := s.findCheck(id)
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	view, err := s.view(check, destinations)
	return &view, err
}

func (s *Service) CreateCheck(user *users_models.User, request CheckRequest) (*CheckView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	check, err := s.checkFrom(request, nil)
	if err != nil {
		return nil, err
	}
	if existing, err := s.repository.checkByName(check.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("a monitor named %q already exists", check.Name)
	}
	check.ID, check.Version, check.CreatedBy = uuid.New(), 1, &user.ID
	check.CreatedAt, check.UpdatedAt = s.now(), s.now()
	if err := s.repository.createCheck(check); err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Synthetic monitor %q created", check.Name))
	return s.checkView(check.ID)
}

func (s *Service) UpdateCheck(user *users_models.User, id uuid.UUID, request CheckRequest) (*CheckView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	existing, err := s.findCheck(id)
	if err != nil {
		return nil, err
	}
	if request.Version != existing.Version {
		return nil, ErrConflict
	}
	check, err := s.checkFrom(request, existing)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(check.Name, existing.Name) {
		if other, err := s.repository.checkByName(check.Name); err != nil {
			return nil, err
		} else if other != nil {
			return nil, fmt.Errorf("a monitor named %q already exists", check.Name)
		}
	}
	check.ID, check.CreatedAt, check.CreatedBy, check.UpdatedAt = existing.ID, existing.CreatedAt, existing.CreatedBy, s.now()
	updated, err := s.repository.updateCheck(check, existing.Version)
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, ErrConflict
	}
	s.audit(user, fmt.Sprintf("Synthetic monitor %q updated", check.Name))
	return s.checkView(id)
}

func (s *Service) SetCheckEnabled(user *users_models.User, id uuid.UUID, request EnabledRequest) (*CheckView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	check, err := s.findCheck(id)
	if err != nil {
		return nil, err
	}
	if request.Version != 0 && request.Version != check.Version {
		return nil, ErrConflict
	}
	expected := check.Version
	check.Enabled, check.UpdatedAt = request.Enabled, s.now()
	if updated, err := s.repository.updateCheck(check, expected); err != nil {
		return nil, err
	} else if !updated {
		return nil, ErrConflict
	}
	state := "disabled"
	if request.Enabled {
		state = "enabled"
	}
	s.audit(user, fmt.Sprintf("Synthetic monitor %q %s", check.Name, state))
	return s.checkView(id)
}

// CloneCheck copies a monitor, secrets included (still encrypted), as a
// disabled "<name> (copy)".
func (s *Service) CloneCheck(user *users_models.User, id uuid.UUID) (*CheckView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	original, err := s.findCheck(id)
	if err != nil {
		return nil, err
	}
	name, err := s.freeName(original.Name + " (copy)")
	if err != nil {
		return nil, err
	}
	clone := *original
	clone.ID, clone.Name, clone.Enabled, clone.Version, clone.CreatedBy = uuid.New(), name, false, 1, &user.ID
	clone.CreatedAt, clone.UpdatedAt = s.now(), s.now()
	if err := s.repository.createCheck(&clone); err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Synthetic monitor %q cloned as %q", original.Name, name))
	return s.checkView(clone.ID)
}

func (s *Service) DeleteCheck(user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	check, err := s.findCheck(id)
	if err != nil {
		return err
	}
	if err := s.repository.deleteCheck(id); err != nil {
		return err
	}
	s.audit(user, fmt.Sprintf("Synthetic monitor %q deleted", check.Name))
	return nil
}

// Destinations

func (s *Service) ListDestinations(user *users_models.User) ([]*Destination, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	return s.repository.destinations()
}

func (s *Service) CreateDestination(user *users_models.User, request DestinationRequest) (*Destination, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(request.APIKey) == "" {
		return nil, errors.New("the project's API key is required")
	}
	destination := &Destination{ID: uuid.New(), CreatedBy: &user.ID, CreatedAt: s.now()}
	if err := s.fillDestination(destination, request); err != nil {
		return nil, err
	}
	if err := s.repository.saveDestination(destination); err != nil {
		return nil, uniqueError(err, "a destination named %q already exists", destination.Name)
	}
	s.audit(user, fmt.Sprintf("Synthetics destination %q created", destination.Name))
	return destination, nil
}

func (s *Service) UpdateDestination(
	user *users_models.User,
	id uuid.UUID,
	request DestinationRequest,
) (*Destination, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	destination, err := s.repository.destination(id)
	if err != nil {
		return nil, notFound(err)
	}
	if err := s.fillDestination(destination, request); err != nil {
		return nil, err
	}
	if err := s.repository.saveDestination(destination); err != nil {
		return nil, uniqueError(err, "a destination named %q already exists", destination.Name)
	}
	s.audit(user, fmt.Sprintf("Synthetics destination %q updated", destination.Name))
	return destination, nil
}

func (s *Service) DeleteDestination(user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	destination, err := s.repository.destination(id)
	if err != nil {
		return notFound(err)
	}
	if used, err := s.repository.checksUsingDestination(id); err != nil {
		return err
	} else if used > 0 {
		return ErrInUse
	}
	if used, err := cloudConnectionsUsing("destination_id", id); err != nil {
		return err
	} else if used > 0 {
		return ErrCloudInUse
	}
	if err := s.repository.deleteDestination(id); err != nil {
		return err
	}
	s.audit(user, fmt.Sprintf("Synthetics destination %q deleted", destination.Name))
	return nil
}

// Probes

func (s *Service) ListProbes(user *users_models.User) ([]ProbeView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	probes, err := s.repository.probes()
	if err != nil {
		return nil, err
	}
	checks, err := s.repository.enabledChecks()
	if err != nil {
		return nil, err
	}
	// Members that left stay listed until they are forgotten.
	members, err := s.repository.members(s.now().Add(-memberForgetAfter))
	if err != nil {
		return nil, err
	}
	views := make([]ProbeView, 0, len(probes))
	for _, probe := range probes {
		view := ProbeView{Probe: probe, Online: s.isOnline(probe), Members: []MemberView{}}
		for _, member := range members {
			if member.ProbeID == probe.ID {
				live := s.now().Sub(member.LastSeenAt) <= memberLiveFor
				view.Members = append(view.Members, MemberView{ProbeMember: *member, Live: live})
			}
		}
		for _, check := range checks {
			if check.runsAt(probe.Location) {
				view.Checks++
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) CreateProbe(user *users_models.User, request ProbeRequest) (*ProbeTokenResponse, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	name, location := strings.TrimSpace(request.Name), strings.ToLower(strings.TrimSpace(request.Location))
	if name == "" {
		return nil, errors.New("name is required")
	}
	if !locationPattern.MatchString(location) {
		return nil, errors.New("location must be lowercase letters, digits, '.', '_' or '-' (e.g. aws-ap-south-1)")
	}
	probe := &Probe{
		ID: uuid.New(), Name: name, Location: location, Description: strings.TrimSpace(request.Description),
		CreatedBy: &user.ID, CreatedAt: s.now(), UpdatedAt: s.now(),
	}
	raw, token, err := s.newToken(probe.ID)
	if err != nil {
		return nil, err
	}
	if err := s.repository.createProbe(probe, token); err != nil {
		return nil, uniqueError(err, "a probe named %q already exists", name)
	}
	s.audit(user, fmt.Sprintf("Synthetics probe %q (%s) created", name, location))
	return &ProbeTokenResponse{Probe: probe, Token: raw}, nil
}

// RotateProbeToken issues a new token; old ones work for tokenGrace so running members can move
// over, or stop at once with revokeOld (a leaked token).
func (s *Service) RotateProbeToken(
	user *users_models.User, id uuid.UUID, revokeOld bool,
) (*ProbeTokenResponse, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	probe, err := s.repository.probe(id)
	if err != nil {
		return nil, notFound(err)
	}
	raw, token, err := s.newToken(id)
	if err != nil {
		return nil, err
	}
	revoked, err := s.repository.replaceToken(id, token, s.now().Add(tokenGrace), revokeOld)
	if err != nil {
		return nil, err
	}
	if revokeOld {
		forgetProbeTokens(revoked)
		s.audit(user, fmt.Sprintf("Synthetics probe group %q: new token; the old ones revoked", probe.Name))
	} else {
		s.audit(user, fmt.Sprintf("Synthetics probe group %q: new token; the old one works for 24 hours", probe.Name))
	}
	return &ProbeTokenResponse{Probe: probe, Token: raw}, nil
}

func (s *Service) DeleteProbe(user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	probe, err := s.repository.probe(id)
	if err != nil {
		return notFound(err)
	}
	if used, err := cloudConnectionsUsing("probe_id", id); err != nil {
		return err
	} else if used > 0 {
		return ErrCloudInUse
	}
	hashes, err := s.repository.deleteProbe(id)
	if err != nil {
		return err
	}
	forgetProbeTokens(hashes)
	s.audit(user, fmt.Sprintf("Synthetics probe %q deleted", probe.Name))
	return nil
}

// checkFrom validates request into a Check. existing (an update) lends its
// stored secrets for the names request leaves out.
func (s *Service) checkFrom(request CheckRequest, existing *Check) (*Check, error) {
	monitor := spec.Monitor{
		Name: request.Name, Enabled: request.Enabled, IntervalSeconds: request.IntervalSeconds,
		Locations: request.Locations, DownLocations: request.DownLocations, Definition: request.Definition,
		Folder: request.Folder,
	}
	if request.Alerts != nil {
		monitor.Alerts = &spec.MonitorAlerts{
			Notify:        request.Alerts.Notify,
			CertDays:      request.Alerts.CertDays,
			RepeatSeconds: request.Alerts.RepeatSeconds,
		}
	}
	if err := monitor.Normalize(); err != nil {
		return nil, err
	}
	var existingID uuid.UUID
	if existing != nil {
		existingID = existing.ID
	}
	parents, err := s.checkParents(existingID, request.ParentIDs)
	if err != nil {
		return nil, err
	}
	alerts, err := checkAlerts(monitor.Alerts, request.Alerts)
	if err != nil {
		return nil, err
	}
	if _, err := s.repository.destination(request.DestinationID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("choose where the results go (a destination)")
		}
		return nil, err
	}
	secrets := map[string]string{}
	if existing != nil {
		stored, err := s.decryptSecrets(existing.SecretsEncrypted)
		if err != nil {
			return nil, err
		}
		secrets = stored
	}
	for name, value := range request.Secrets {
		if value == "" {
			delete(secrets, name)
		} else {
			secrets[name] = value
		}
	}
	encrypted, err := s.encryptSecrets(secrets, monitor.Definition.Placeholders())
	if err != nil {
		return nil, err
	}
	return &Check{
		Name: monitor.Name, Type: monitor.Definition.Type, Enabled: monitor.Enabled,
		IntervalSeconds: monitor.IntervalSeconds, DownLocations: monitor.DownLocations,
		Locations: monitor.Locations, DestinationID: request.DestinationID,
		Definition: DefinitionJSON(monitor.Definition), SecretsEncrypted: encrypted,
		Folder: monitor.Folder, ParentIDs: parents, Alerts: alerts,
	}, nil
}

func (s *Service) view(check *Check, destinations map[uuid.UUID]*Destination) (CheckView, error) {
	view := CheckView{
		ID: check.ID, Name: check.Name, Type: check.Type, Enabled: check.Enabled,
		IntervalSeconds: check.IntervalSeconds, DownLocations: check.DownLocations, Locations: check.Locations,
		Definition: spec.Definition(check.Definition), Version: check.Version,
		CreatedAt: check.CreatedAt, UpdatedAt: check.UpdatedAt,
		Folder: check.Folder, ParentIDs: check.ParentIDs, Alerts: check.Alerts,
	}
	if view.Locations == nil {
		view.Locations = []string{}
	}
	if view.ParentIDs == nil {
		view.ParentIDs = []uuid.UUID{}
	}
	if view.Alerts.ChannelIDs == nil {
		view.Alerts.ChannelIDs = []uuid.UUID{}
	}
	if destination, ok := destinations[check.DestinationID]; ok {
		view.Destination = DestinationRef{
			ID:        destination.ID,
			Name:      destination.Name,
			SiteID:    destination.SiteID,
			ProjectID: destination.ProjectID,
		}
	}
	stored, err := s.decryptSecrets(check.SecretsEncrypted)
	if err != nil {
		return view, err
	}
	view.Secrets = []SecretState{}
	for _, name := range view.Definition.Placeholders() {
		_, ok := stored[name]
		view.Secrets = append(view.Secrets, SecretState{Name: name, Stored: ok})
	}
	return view, nil
}

// summary is a monitor as non-admins see it: enough to list it and read its results, without
// headers, bodies, assertions or secret names.
func (s *Service) summary(check *Check, destinations map[uuid.UUID]*Destination) CheckView {
	view := CheckView{
		ID: check.ID, Name: check.Name, Type: check.Type, Enabled: check.Enabled,
		IntervalSeconds: check.IntervalSeconds, DownLocations: check.DownLocations, Locations: check.Locations,
		Version: check.Version, CreatedAt: check.CreatedAt, UpdatedAt: check.UpdatedAt,
		Folder: check.Folder, ParentIDs: check.ParentIDs, Alerts: check.Alerts, Secrets: []SecretState{},
	}
	full := spec.Definition(check.Definition)
	view.Definition = spec.Definition{Type: full.Type, Target: full.Target, TimeoutSeconds: full.TimeoutSeconds}
	if full.Request != nil {
		view.Definition.Request = &spec.HTTPRequest{Method: full.Request.Method, URL: full.Request.URL}
	}
	for _, step := range full.Steps {
		view.Definition.Steps = append(view.Definition.Steps, spec.Step{
			Name: step.Name, Request: spec.HTTPRequest{Method: step.Request.Method, URL: step.Request.URL},
		})
	}
	if full.TLS != nil {
		options := *full.TLS
		view.Definition.TLS = &options
	}
	if view.Locations == nil {
		view.Locations = []string{}
	}
	if view.ParentIDs == nil {
		view.ParentIDs = []uuid.UUID{}
	}
	if view.Alerts.ChannelIDs == nil {
		view.Alerts.ChannelIDs = []uuid.UUID{}
	}
	if destination, ok := destinations[check.DestinationID]; ok {
		view.Destination = DestinationRef{
			ID: destination.ID, Name: destination.Name, SiteID: destination.SiteID, ProjectID: destination.ProjectID,
		}
	}
	return view
}

func (s *Service) fillDestination(destination *Destination, request DestinationRequest) error {
	destination.Name = strings.TrimSpace(request.Name)
	if destination.Name == "" {
		return errors.New("name is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(request.OTLPURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New(
			"the OTLP URL must be the Site's http(s) address as probes reach it, e.g. https://obzly.example.com",
		)
	}
	destination.OTLPURL = strings.TrimRight(parsed.String(), "/")
	destination.SiteID, destination.ProjectID = strings.TrimSpace(request.SiteID), strings.TrimSpace(request.ProjectID)
	if key := strings.TrimSpace(request.APIKey); key != "" {
		encrypted, err := s.secrets.Encrypt(key)
		if err != nil {
			return err
		}
		destination.APIKeyEncrypted = encrypted
		destination.APIKeyHint = keyHint(key)
	}
	destination.UpdatedAt = s.now()
	return nil
}

func (s *Service) newToken(probeID uuid.UUID) (string, *ProbeToken, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	raw := spec.TokenPrefix + hex.EncodeToString(random)
	return raw, &ProbeToken{
		ID: uuid.New(), ProbeID: probeID, TokenHash: access_tokens.HashToken(raw),
		TokenPrefix: raw[:len(spec.TokenPrefix)+6], CreatedAt: s.now(),
	}, nil
}

func (s *Service) isOnline(probe *Probe) bool {
	return probe.LastSeenAt != nil && s.now().Sub(*probe.LastSeenAt) < onlineWithin
}

func (s *Service) findCheck(id uuid.UUID) (*Check, error) {
	check, err := s.repository.check(id)
	return check, notFound(err)
}

func (s *Service) destinationsByID() (map[uuid.UUID]*Destination, error) {
	destinations, err := s.repository.destinations()
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]*Destination, len(destinations))
	for _, destination := range destinations {
		byID[destination.ID] = destination
	}
	return byID, nil
}

// freeName is name, or name with " 2", " 3"... when taken.
func (s *Service) freeName(name string) (string, error) {
	candidate := name
	for i := 2; ; i++ {
		existing, err := s.repository.checkByName(candidate)
		if err != nil || existing == nil {
			return candidate, err
		}
		candidate = fmt.Sprintf("%s %d", name, i)
	}
}

// encryptSecrets keeps only values for placeholders the definition uses.
func (s *Service) encryptSecrets(values map[string]string, used []string) (string, error) {
	kept := map[string]string{}
	for _, name := range used {
		if value, ok := values[name]; ok {
			kept[name] = value
		}
	}
	if len(kept) == 0 {
		return "", nil
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return "", err
	}
	return s.secrets.Encrypt(string(data))
}

func (s *Service) decryptSecrets(encrypted string) (map[string]string, error) {
	values := map[string]string{}
	if encrypted == "" {
		return values, nil
	}
	plain, err := s.secrets.Decrypt(encrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt monitor secrets: %w", err)
	}
	return values, json.Unmarshal([]byte(plain), &values)
}

func (s *Service) audit(user *users_models.User, message string) {
	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(message, &user.ID, nil)
	}
}

func keyHint(key string) string {
	if len(key) <= 8 {
		return "…"
	}
	return key[:3] + "…" + key[len(key)-4:]
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func uniqueError(err error, format string, name string) error {
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return fmt.Errorf(format, name)
	}
	return err
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
