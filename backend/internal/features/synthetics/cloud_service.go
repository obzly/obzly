package synthetics

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"gorm.io/gorm"

	"obzly/internal/cache"
	"obzly/internal/config"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

// CloudConnectionView is a connection as admins see it.
type CloudConnectionView struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	Provider        string         `json:"provider"`
	Scope           string         `json:"scope"`
	ScopeID         string         `json:"scopeId"`
	Access          string         `json:"access"`
	Identity        string         `json:"identity"`
	ExternalID      string         `json:"externalId"`
	TenantID        string         `json:"tenantId"`
	Probe           ProbeRef       `json:"probe"`
	Destination     DestinationRef `json:"destination"`
	ScanTypes       []string       `json:"scanTypes"`
	Regions         []string       `json:"regions"`
	ResourceTypes   []string       `json:"resourceTypes"`
	IntervalSeconds int            `json:"intervalSeconds"`
	// HealthIntervalSeconds between health scans of what discovery found.
	HealthIntervalSeconds int        `json:"healthIntervalSeconds"`
	Enabled               bool       `json:"enabled"`
	Status                CloudState `json:"status"`
	Version               int        `json:"version"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

type ProbeRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Location string    `json:"location"`
}

type CloudConnectionRequest struct {
	Name            string    `json:"name"`
	Provider        string    `json:"provider"`
	Scope           string    `json:"scope"`
	ScopeID         string    `json:"scopeId"`
	Access          string    `json:"access"`
	Identity        string    `json:"identity"`
	ExternalID      string    `json:"externalId"`
	TenantID        string    `json:"tenantId"`
	ProbeID         uuid.UUID `json:"probeId"`
	DestinationID   uuid.UUID `json:"destinationId"`
	ScanTypes       []string  `json:"scanTypes"`
	Regions         []string  `json:"regions"`
	ResourceTypes   []string  `json:"resourceTypes"`
	IntervalSeconds int       `json:"intervalSeconds"`
	// HealthIntervalSeconds: 0 is the default, 15 minutes.
	HealthIntervalSeconds int   `json:"healthIntervalSeconds"`
	Enabled               *bool `json:"enabled"`
	Version               int   `json:"version"`
}

// CloudSetup is what the wizard fills templates with.
type CloudSetup struct {
	// Issuer is the control plane's URL, the OIDC issuer for federated
	// connections; its discovery document must be reachable from the cloud.
	Issuer     string `json:"issuer"`
	ExternalID string `json:"externalId"`
	// Audience per provider for federated tokens.
	Audiences map[string]string `json:"audiences"`
}

// CloudTest is a Test access run: queued on one probe group, done when a
// member reports back.
type CloudTest struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Probe  string          `json:"probe"`
	Result *spec.CloudScan `json:"result,omitempty"`
}

const (
	cloudTestPrefix   = "synthetics:cloud-test:"
	cloudResultPrefix = "synthetics:cloud-result:"
	// cloudTaskTokenPrefix marks a federated Test access task, by its
	// provider, until its result arrives.
	cloudTaskTokenPrefix  = "synthetics:cloud-task-token:"
	cloudTokenLifetime    = 10 * time.Minute
	externalIDRandomBytes = 16
	externalIDMACBytes    = 8

	// Bounds on what a probe reports about a scan (it is stored and shown).
	maxScanErrors      = 20
	maxScanText        = 2000
	maxScanRegions     = 200
	maxScanTypes       = 100
	maxScanUses        = 10
	maxScanMissing     = 100
	maxScanProbeLength = 128
)

// Audiences the clouds expect on a federated token.
var cloudAudiences = map[string]string{
	spec.ProviderAWS:   "sts.amazonaws.com",
	spec.ProviderAzure: "api://AzureADTokenExchange",
}

// Resource types a connection can limit discovery to; none means all.
var cloudResourceTypes = map[string][]string{
	spec.ProviderAWS: {
		"ec2_instance", "load_balancer", "eks_cluster", "rds_instance", "autoscaling_group",
		"lambda_function", "s3_bucket",
	},
	spec.ProviderAzure: {
		"virtual_machine", "vm_scale_set", "load_balancer", "application_gateway", "aks_cluster",
		"sql_database", "postgresql_server", "storage_account", "function_app",
	},
}

var (
	awsAccountPattern     = regexp.MustCompile(`^\d{12}$`)
	awsOrgPattern         = regexp.MustCompile(`^o-[a-z0-9]{10,32}$`)
	awsRolePattern        = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`)
	awsRegionPattern      = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d{1,2}$`)
	azureLocationPattern  = regexp.MustCompile(`^[a-z][a-z0-9]{1,39}$`)
	azureMgmtGroupPattern = regexp.MustCompile(`^[A-Za-z0-9_().-]{1,90}$`)
	externalIDPattern     = regexp.MustCompile(`^[A-Za-z0-9+=,.@:/-]{16,128}$`)

	// cloudTokenSigner signs federated tokens with the control plane's RS256 key; main sets it so
	// this package doesn't import control_plane_services.
	cloudTokenSigner func(subject, audience string, lifetime time.Duration) (string, error)
)

// SetCloudTokenSigner wires the control plane's signer for federated
// cloud connections.
func SetCloudTokenSigner(signer func(subject, audience string, lifetime time.Duration) (string, error)) {
	cloudTokenSigner = signer
}

// ListCloudConnections is every connection; who it signs in as (role or
// client ID, external ID, tenant) only for admins.
func (s *Service) ListCloudConnections(user *users_models.User) ([]CloudConnectionView, error) {
	var connections []*CloudConnection
	if err := storage.GetDb().Order("lower(name)").Find(&connections).Error; err != nil {
		return nil, err
	}
	views, err := s.cloudViews(connections)
	if err != nil || user.CanUpdateSettings() {
		return views, err
	}
	for i := range views {
		views[i] = cloudSummary(views[i])
	}
	return views, nil
}

func (s *Service) GetCloudConnection(user *users_models.User, id uuid.UUID) (*CloudConnectionView, error) {
	view, err := s.cloudConnectionView(id)
	if err != nil || user.CanUpdateSettings() {
		return view, err
	}
	summary := cloudSummary(*view)
	return &summary, nil
}

// CloudSetup gives the wizard a fresh external ID and the issuer.
func (s *Service) CloudSetup(user *users_models.User) (*CloudSetup, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	externalID, err := s.newExternalID()
	if err != nil {
		return nil, err
	}
	return &CloudSetup{
		Issuer: strings.TrimRight(config.GetEnv().PublicURL, "/"), ExternalID: externalID,
		Audiences: cloudAudiences,
	}, nil
}

func (s *Service) CreateCloudConnection(
	user *users_models.User, request CloudConnectionRequest,
) (*CloudConnectionView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	connection, err := s.cloudConnectionFrom(request, nil)
	if err != nil {
		return nil, err
	}
	connection.ID, connection.Version, connection.CreatedBy = uuid.New(), 1, &user.ID
	connection.CreatedAt, connection.UpdatedAt = s.now(), s.now()
	if err := storage.GetDb().Create(connection).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("a cloud connection named %q already exists", connection.Name)
		}
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Cloud connection %q created (%s %s)", connection.Name, connection.Provider,
		connection.ScopeID))
	return s.cloudConnectionView(connection.ID)
}

func (s *Service) UpdateCloudConnection(
	user *users_models.User, id uuid.UUID, request CloudConnectionRequest,
) (*CloudConnectionView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	existing, err := s.findCloudConnection(id)
	if err != nil {
		return nil, err
	}
	if request.Version != existing.Version {
		return nil, ErrConflict
	}
	connection, err := s.cloudConnectionFrom(request, existing)
	if err != nil {
		return nil, err
	}
	connection.Version, connection.UpdatedAt = existing.Version+1, s.now()
	result := storage.GetDb().Model(&CloudConnection{}).Where("id = ? AND version = ?", id, existing.Version).
		Select("*").Omit("id", "created_at", "created_by", "status").Updates(connection)
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return nil, fmt.Errorf("a cloud connection named %q already exists", connection.Name)
		}
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrConflict
	}
	s.audit(user, fmt.Sprintf("Cloud connection %q updated", connection.Name))
	return s.cloudConnectionView(id)
}

func (s *Service) DeleteCloudConnection(user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	connection, err := s.findCloudConnection(id)
	if err != nil {
		return err
	}
	if err := storage.GetDb().Where("id = ?", id).Delete(&CloudConnection{}).Error; err != nil {
		return err
	}
	s.audit(user, fmt.Sprintf("Cloud connection %q deleted", connection.Name))
	return nil
}

// TestCloudConnection queues a dry scan of an unsaved (or saved) connection
// on its probe group; any live member picks it up.
func (s *Service) TestCloudConnection(
	ctx context.Context, user *users_models.User, request CloudConnectionRequest,
) (*CloudTest, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	connection, err := s.cloudConnectionFrom(request, nil)
	if err != nil {
		return nil, err
	}
	connection.ID = uuid.New()
	probe, err := s.repository.probe(connection.ProbeID)
	if err != nil {
		return nil, err
	}
	if !s.isOnline(probe) {
		return nil, fmt.Errorf("no instance of probe group %q is online; start one first", probe.Name)
	}
	wire := connection.wire()
	task := spec.Task{ID: uuid.NewString(), Cloud: &wire}
	encoded, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	test := CloudTest{ID: task.ID, Status: TaskQueued, Probe: probe.Name}
	testJSON, err := json.Marshal(test)
	if err != nil {
		return nil, err
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	queue := taskQueuePrefix + probe.ID.String()
	commands := valkey.Commands{
		client.B().Set().Key(taskOwnerPrefix + task.ID).Value(probe.ID.String()).Ex(taskTTL).Build(),
		client.B().Set().Key(cloudTestPrefix + task.ID).Value(string(testJSON)).Ex(taskTTL).Build(),
	}
	if connection.Access == spec.AccessFederated {
		// The connection isn't saved, so its token is issued for the task.
		commands = append(commands,
			client.B().Set().Key(cloudTaskTokenPrefix+task.ID).Value(connection.Provider).Ex(taskTTL).Build())
	}
	commands = append(commands,
		client.B().Rpush().Key(queue).Element(string(encoded)).Build(),
		client.B().Expire().Key(queue).Seconds(int64(taskTTL.Seconds())).Build(),
	)
	for _, result := range client.DoMulti(ctx, commands...) {
		if err := result.Error(); err != nil {
			return nil, fmt.Errorf("failed to queue the test: %w", err)
		}
	}
	return &test, nil
}

func (s *Service) GetCloudTest(ctx context.Context, user *users_models.User, id string) (*CloudTest, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	encoded, err := client.Do(ctx, client.B().Get().Key(cloudTestPrefix+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var test CloudTest
	if err := json.Unmarshal([]byte(encoded), &test); err != nil {
		return nil, err
	}
	result, err := client.Do(ctx, client.B().Get().Key(cloudResultPrefix+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return &test, nil
	}
	if err != nil {
		return nil, err
	}
	var scan spec.CloudScan
	if json.Unmarshal([]byte(result), &scan) == nil {
		test.Status, test.Result = TaskDone, &scan
	}
	return &test, nil
}

// PostCloudTestResult stores a Test access result, if the task was this
// probe group's.
func (s *Service) PostCloudTestResult(ctx context.Context, probe *Probe, taskID string, scan spec.CloudScan) error {
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	owner, err := client.Do(ctx, client.B().Get().Key(taskOwnerPrefix+taskID).Build()).ToString()
	if valkey.IsValkeyNil(err) || (err == nil && owner != probe.ID.String()) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(boundedScan(scan))
	if err != nil {
		return err
	}
	for _, result := range client.DoMulti(ctx,
		client.B().Set().Key(cloudResultPrefix+taskID).Value(string(encoded)).Ex(taskTTL).Build(),
		client.B().Del().Key(cloudTaskTokenPrefix+taskID).Build(),
	) {
		if err := result.Error(); err != nil {
			return err
		}
	}
	return nil
}

// TaskCloudToken signs a federated token for a Test access task of probe's group that is still
// waiting for its result.
func (s *Service) TaskCloudToken(ctx context.Context, probe *Probe, taskID string) (*spec.CloudToken, error) {
	if cloudTokenSigner == nil {
		return nil, errors.New("this deployment can't issue cloud tokens")
	}
	client := cache.GetCache()
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	replies := client.DoMulti(ctx,
		client.B().Get().Key(taskOwnerPrefix+taskID).Build(),
		client.B().Get().Key(cloudTaskTokenPrefix+taskID).Build(),
	)
	owner, err := replies[0].ToString()
	if valkey.IsValkeyNil(err) || (err == nil && owner != probe.ID.String()) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	provider, err := replies[1].ToString()
	if valkey.IsValkeyNil(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.signCloudToken(probe, provider)
}

// ReportCloudScan records a scheduled scan's outcome on its connection,
// keeping the latest of each kind.
func (s *Service) ReportCloudScan(probe *Probe, id uuid.UUID, scan spec.CloudScan) error {
	connection, err := s.findCloudConnection(id)
	if err != nil {
		return err
	}
	if connection.ProbeID != probe.ID {
		return ErrNotFound
	}
	scan = boundedScan(scan)
	now := s.now()
	state := connection.Status
	state.ReportedAt = &now
	if scan.Kind == spec.ScanHealth {
		state.LastHealth = &scan
	} else {
		state.LastScan = &scan
	}
	return storage.GetDb().Model(&CloudConnection{}).Where("id = ?", id).Update("status", state).Error
}

// CloudToken signs a short-lived token a federated connection's probe exchanges for cloud
// credentials: subject probe:<group ID>, audience the provider's.
func (s *Service) CloudToken(probe *Probe, id uuid.UUID) (*spec.CloudToken, error) {
	if cloudTokenSigner == nil {
		return nil, errors.New("this deployment can't issue cloud tokens")
	}
	connection, err := s.findCloudConnection(id)
	if err != nil {
		return nil, err
	}
	if connection.ProbeID != probe.ID || connection.Access != spec.AccessFederated {
		return nil, ErrNotFound
	}
	return s.signCloudToken(probe, connection.Provider)
}

// CloudSubject is the subject the cloud's trust policy names: the group's ID, not its name, so a
// group deleted and recreated under the same name isn't trusted.
func CloudSubject(probeID uuid.UUID) string {
	return "probe:" + probeID.String()
}

func (s *Service) signCloudToken(probe *Probe, provider string) (*spec.CloudToken, error) {
	audience, ok := cloudAudiences[provider]
	if !ok {
		return nil, ErrNotFound
	}
	token, err := cloudTokenSigner(CloudSubject(probe.ID), audience, cloudTokenLifetime)
	if err != nil {
		return nil, err
	}
	return &spec.CloudToken{Token: token, ExpiresAt: s.now().Add(cloudTokenLifetime)}, nil
}

// cloudConnectionsFor is what probe's group scans, for its config.
func (s *Service) cloudConnectionsFor(probe *Probe) ([]*CloudConnection, error) {
	var connections []*CloudConnection
	err := storage.GetDb().Where("probe_id = ? AND enabled", probe.ID).Order("id").Find(&connections).Error
	return connections, err
}

// cloudConnectionsUsing counts connections whose column (probe_id or
// destination_id, never user input) is id.
func cloudConnectionsUsing(column string, id uuid.UUID) (int64, error) {
	var count int64
	err := storage.GetDb().Model(&CloudConnection{}).Where(column+" = ?", id).Count(&count).Error
	return count, err
}

func (s *Service) findCloudConnection(id uuid.UUID) (*CloudConnection, error) {
	var connection CloudConnection
	err := storage.GetDb().Where("id = ?", id).First(&connection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &connection, nil
}

func (s *Service) cloudViews(connections []*CloudConnection) ([]CloudConnectionView, error) {
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	probes, err := s.repository.probes()
	if err != nil {
		return nil, err
	}
	probesByID := map[uuid.UUID]*Probe{}
	for _, probe := range probes {
		probesByID[probe.ID] = probe
	}
	views := make([]CloudConnectionView, 0, len(connections))
	for _, c := range connections {
		view := CloudConnectionView{
			ID: c.ID, Name: c.Name, Provider: c.Provider, Scope: c.Scope, ScopeID: c.ScopeID, Access: c.Access,
			Identity: c.Identity, ExternalID: c.ExternalID, TenantID: c.TenantID,
			ScanTypes: orEmpty(c.ScanTypes), Regions: orEmpty(c.Regions), ResourceTypes: orEmpty(c.ResourceTypes),
			IntervalSeconds: c.IntervalSeconds, HealthIntervalSeconds: c.HealthIntervalSeconds,
			Enabled: c.Enabled, Status: c.Status, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		}
		if probe, ok := probesByID[c.ProbeID]; ok {
			view.Probe = ProbeRef{ID: probe.ID, Name: probe.Name, Location: probe.Location}
		}
		if destination, ok := destinations[c.DestinationID]; ok {
			view.Destination = DestinationRef{
				ID:        destination.ID,
				Name:      destination.Name,
				SiteID:    destination.SiteID,
				ProjectID: destination.ProjectID,
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// cloudConnectionFrom validates request into a connection. The external ID is always the control
// plane's (kept, issued by CloudSetup, or fresh), never one the caller made up.
func (s *Service) cloudConnectionFrom(
	request CloudConnectionRequest,
	existing *CloudConnection,
) (*CloudConnection, error) {
	c := &CloudConnection{
		Name:     strings.TrimSpace(request.Name),
		Provider: request.Provider,
		Scope:    request.Scope,
		ScopeID: strings.TrimSpace(
			request.ScopeID,
		),
		Access:                request.Access,
		Identity:              strings.TrimSpace(request.Identity),
		TenantID:              strings.ToLower(strings.TrimSpace(request.TenantID)),
		ProbeID:               request.ProbeID,
		DestinationID:         request.DestinationID,
		IntervalSeconds:       request.IntervalSeconds,
		HealthIntervalSeconds: request.HealthIntervalSeconds,
		Enabled:               request.Enabled == nil || *request.Enabled,
	}
	if existing != nil {
		c.ID, c.CreatedAt, c.CreatedBy, c.Status = existing.ID, existing.CreatedAt, existing.CreatedBy, existing.Status
		c.ExternalID = existing.ExternalID
	}
	if c.ExternalID == "" && c.Access == spec.AccessRole {
		requested := strings.TrimSpace(request.ExternalID)
		if s.issuedExternalID(requested) {
			c.ExternalID = requested
		} else {
			fresh, err := s.newExternalID()
			if err != nil {
				return nil, err
			}
			c.ExternalID = fresh
		}
	}
	if c.Name == "" || len(c.Name) > 100 {
		return nil, errors.New("name must be 1 to 100 characters")
	}
	if err := cloudIntervals(c); err != nil {
		return nil, err
	}
	uses, err := cloudUses(request.ScanTypes)
	if err != nil {
		return nil, err
	}
	c.ScanTypes = uses
	switch c.Provider {
	case spec.ProviderAWS:
		err = validateAWS(c)
	case spec.ProviderAzure:
		err = validateAzure(c)
	default:
		err = errors.New("provider must be aws or azure")
	}
	if err != nil {
		return nil, err
	}
	if c.Regions, err = cloudRegions(c.Provider, request.Regions); err != nil {
		return nil, err
	}
	if c.ResourceTypes, err = cloudTypes(c.Provider, request.ResourceTypes); err != nil {
		return nil, err
	}
	if _, err := s.repository.probe(c.ProbeID); err != nil {
		return nil, errors.New("pick the probe group that runs the scans")
	}
	if _, err := s.repository.destination(c.DestinationID); err != nil {
		return nil, errors.New("pick where results go (a destination)")
	}
	return c, nil
}

func validateAWS(c *CloudConnection) error {
	switch c.Scope {
	case "account":
		if !awsAccountPattern.MatchString(c.ScopeID) {
			return errors.New("an AWS account ID is 12 digits")
		}
	case "organization":
		if !awsOrgPattern.MatchString(c.ScopeID) {
			return errors.New("an AWS Organization ID looks like o-a1b2c3d4e5")
		}
		if c.Access == spec.AccessProbe {
			return errors.New("a whole Organization needs a role in each account: pick a role or federated access")
		}
	default:
		return errors.New("scope must be account or organization")
	}
	c.TenantID = ""
	switch c.Access {
	case spec.AccessProbe:
		c.Identity, c.ExternalID = "", ""
	case spec.AccessRole:
		if !awsRolePattern.MatchString(c.Identity) {
			return errors.New("the role ARN looks like arn:aws:iam::123456789012:role/obzly-readonly")
		}
		if !externalIDPattern.MatchString(c.ExternalID) {
			return errors.New("the external ID must be 16 to 128 letters, digits or +=,.@:/-")
		}
	case spec.AccessFederated:
		if !awsRolePattern.MatchString(c.Identity) {
			return errors.New("the role ARN looks like arn:aws:iam::123456789012:role/obzly-readonly")
		}
		c.ExternalID = ""
	default:
		return errors.New("access for AWS must be probe, role or federated")
	}
	return nil
}

func validateAzure(c *CloudConnection) error {
	switch c.Scope {
	case "subscription":
		if _, err := uuid.Parse(c.ScopeID); err != nil {
			return errors.New("an Azure subscription ID is a GUID")
		}
		c.ScopeID = strings.ToLower(c.ScopeID)
	case "management_group":
		if !azureMgmtGroupPattern.MatchString(c.ScopeID) {
			return errors.New("a management group ID is up to 90 letters, digits or _().-")
		}
	default:
		return errors.New("scope must be subscription or management_group")
	}
	if _, err := uuid.Parse(c.TenantID); err != nil {
		return errors.New("the Microsoft Entra tenant ID is a GUID")
	}
	c.ExternalID = ""
	switch c.Access {
	case spec.AccessProbe:
		// A user-assigned managed identity needs its client ID; a
		// system-assigned one doesn't.
		if c.Identity != "" {
			if _, err := uuid.Parse(c.Identity); err != nil {
				return errors.New("the managed identity's client ID is a GUID")
			}
		}
	case spec.AccessWorkload, spec.AccessFederated:
		if _, err := uuid.Parse(c.Identity); err != nil {
			return errors.New("the identity's client ID is a GUID")
		}
	default:
		return errors.New("access for Azure must be probe, workload or federated")
	}
	c.Identity = strings.ToLower(c.Identity)
	return nil
}

func cloudUses(requested []string) (Locations, error) {
	uses := Locations{}
	for _, use := range requested {
		switch use {
		case spec.UseInventory:
		case "cost", "posture":
			return nil, fmt.Errorf("the %s scan comes in a later release", use)
		default:
			return nil, fmt.Errorf("unknown scan type %q", use)
		}
		if !slices.Contains(uses, use) {
			uses = append(uses, use)
		}
	}
	if len(uses) == 0 {
		return nil, errors.New("pick at least one scan type")
	}
	return uses, nil
}

func cloudRegions(provider string, requested []string) (Locations, error) {
	pattern := awsRegionPattern
	if provider == spec.ProviderAzure {
		pattern = azureLocationPattern
	}
	regions := Locations{}
	for _, region := range requested {
		region = strings.ToLower(strings.TrimSpace(region))
		if region == "" || slices.Contains(regions, region) {
			continue
		}
		if !pattern.MatchString(region) {
			return nil, fmt.Errorf("%q isn't a region name", region)
		}
		regions = append(regions, region)
	}
	return regions, nil
}

func cloudTypes(provider string, requested []string) (Locations, error) {
	types := Locations{}
	for _, kind := range requested {
		if !slices.Contains(cloudResourceTypes[provider], kind) {
			return nil, fmt.Errorf("unknown %s resource type %q", provider, kind)
		}
		if !slices.Contains(types, kind) {
			types = append(types, kind)
		}
	}
	return types, nil
}

// newExternalID is obz-<random>-<MAC of it>: unique per connection, and signed so a later create
// can tell it came from here.
func (s *Service) newExternalID() (string, error) {
	buffer := make([]byte, externalIDRandomBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(buffer)
	mac, err := s.externalIDMAC(nonce)
	if err != nil {
		return "", err
	}
	return "obz-" + nonce + "-" + mac, nil
}

func (s *Service) issuedExternalID(value string) bool {
	nonce, mac, ok := strings.Cut(strings.TrimPrefix(value, "obz-"), "-")
	if !ok || !strings.HasPrefix(value, "obz-") || len(nonce) != 2*externalIDRandomBytes {
		return false
	}
	expected, err := s.externalIDMAC(nonce)
	return err == nil && hmac.Equal([]byte(mac), []byte(expected))
}

func (s *Service) externalIDMAC(nonce string) (string, error) {
	key, err := s.secrets.DeriveKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("cloud-external-id:" + nonce))
	return hex.EncodeToString(mac.Sum(nil)[:externalIDMACBytes]), nil
}

// cloudSummary is a connection as non-admins see it: what and where it
// scans and how that went, not who it signs in as.
func cloudSummary(view CloudConnectionView) CloudConnectionView {
	view.Identity, view.ExternalID, view.TenantID = "", "", ""
	return view
}

func (s *Service) cloudConnectionView(id uuid.UUID) (*CloudConnectionView, error) {
	connection, err := s.findCloudConnection(id)
	if err != nil {
		return nil, err
	}
	views, err := s.cloudViews([]*CloudConnection{connection})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// boundedScan trims what a probe reports to sizes worth storing and
// showing.
func boundedScan(scan spec.CloudScan) spec.CloudScan {
	clip := func(text string) string {
		if len(text) > maxScanText {
			return text[:maxScanText]
		}
		return text
	}
	if len(scan.Errors) > maxScanErrors {
		scan.Errors = scan.Errors[:maxScanErrors]
	}
	for i := range scan.Errors {
		scan.Errors[i] = clip(scan.Errors[i])
	}
	if len(scan.Regions) > maxScanRegions {
		scan.Regions = scan.Regions[:maxScanRegions]
	}
	if len(scan.Types) > maxScanTypes {
		kept := make(map[string]int, maxScanTypes)
		for _, name := range slices.Sorted(maps.Keys(scan.Types))[:maxScanTypes] {
			kept[name] = scan.Types[name]
		}
		scan.Types = kept
	}
	uses := make(map[string]spec.UseResult, len(scan.Uses))
	for _, name := range slices.Sorted(maps.Keys(scan.Uses)) {
		if len(uses) == maxScanUses {
			break
		}
		use := scan.Uses[name]
		use.Message = clip(use.Message)
		if len(use.Missing) > maxScanMissing {
			use.Missing = use.Missing[:maxScanMissing]
		}
		uses[name] = use
	}
	scan.Uses = uses
	if len(scan.Probe) > maxScanProbeLength {
		scan.Probe = scan.Probe[:maxScanProbeLength]
	}
	return scan
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// cloudIntervals fills in and checks the two scan intervals: health scans
// re-read what discovery found, so they can't be rarer than discovery.
func cloudIntervals(c *CloudConnection) error {
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = spec.DefaultCloudInventorySeconds
	}
	if c.HealthIntervalSeconds == 0 {
		c.HealthIntervalSeconds = min(spec.DefaultCloudHealthSeconds, c.IntervalSeconds)
	}
	switch {
	case c.IntervalSeconds < spec.MinCloudInventorySeconds || c.IntervalSeconds > spec.MaxCloudInventorySeconds:
		return fmt.Errorf("the discovery interval must be %s to %s",
			duration(spec.MinCloudInventorySeconds), duration(spec.MaxCloudInventorySeconds))
	case c.HealthIntervalSeconds < spec.MinCloudHealthSeconds || c.HealthIntervalSeconds > spec.MaxCloudHealthSeconds:
		return fmt.Errorf("the health scan interval must be %s to %s",
			duration(spec.MinCloudHealthSeconds), duration(spec.MaxCloudHealthSeconds))
	case c.HealthIntervalSeconds > c.IntervalSeconds:
		return errors.New("health scans can't be less frequent than discovery")
	}
	return nil
}

func duration(seconds int) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d h", seconds/3600)
	}
	return fmt.Sprintf("%d min", seconds/60)
}
