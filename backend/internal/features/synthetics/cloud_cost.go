package synthetics

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

// CostConfig is a connection's cost settings in the cost_settings column (NULL without the cost
// use).
type CostConfig struct {
	Settings *spec.CostSettings
}

// CloudCostSettings are the control plane's cost choices for every connection.
type CloudCostSettings struct {
	// TrackedTags: the tag keys kept on cost rows, to group and budget by.
	TrackedTags []string `json:"trackedTags"`
}

type cloudCostSettingsRow struct {
	ID          int       `gorm:"column:id;primaryKey"`
	TrackedTags Locations `gorm:"column:tracked_tags"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (cloudCostSettingsRow) TableName() string { return "cloud_cost_settings" }

// ErrCostReadThrough: other connections read their cost through this one.
var ErrCostReadThrough = errors.New("change the connections that read their cost through this one first")

var (
	s3BucketPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	exportPrefixPattern   = regexp.MustCompile(`^[A-Za-z0-9!_.*'()/=-]{0,512}$`)
	storageAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	containerPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])$`)
	// Cloud tag keys: AWS allows up to 128 characters of letters, digits,
	// spaces and _.:/=+-@; Azure up to 512 without <>%&\?/.
	tagKeyPattern = regexp.MustCompile(`^[\p{L}\p{N} _.:/=+@-]{1,128}$`)
)

func (c CostConfig) Value() (driver.Value, error) {
	if c.Settings == nil {
		return nil, nil
	}
	return jsonValue(c.Settings)
}

func (c *CostConfig) Scan(value any) error {
	c.Settings = nil
	if value == nil {
		return nil
	}
	var settings spec.CostSettings
	if err := scanJSON(value, &settings); err != nil {
		return err
	}
	c.Settings = &settings
	return nil
}

// GetCloudCostSettings is readable by everyone: the Cost tab shows which tags it tracks.
func (s *Service) GetCloudCostSettings() (*CloudCostSettings, error) {
	tags, err := trackedCostTags()
	if err != nil {
		return nil, err
	}
	return &CloudCostSettings{TrackedTags: tags}, nil
}

func (s *Service) UpdateCloudCostSettings(
	user *users_models.User, request CloudCostSettings,
) (*CloudCostSettings, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	tags := Locations{}
	for _, tag := range request.TrackedTags {
		tag = strings.TrimSpace(tag)
		if tag == "" || slices.Contains(tags, tag) {
			continue
		}
		if !tagKeyPattern.MatchString(tag) {
			return nil, fmt.Errorf("%q isn't a tag key: up to 128 letters, digits, spaces or _.:/=+@-", tag)
		}
		tags = append(tags, tag)
	}
	if len(tags) > spec.MaxCostTrackedTags {
		return nil, fmt.Errorf("track at most %d tag keys", spec.MaxCostTrackedTags)
	}
	row := cloudCostSettingsRow{ID: 1, TrackedTags: tags, UpdatedAt: s.now()}
	err := storage.GetDb().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"tracked_tags", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Tracked cost tags set to %s", strings.Join(tags, ", ")))
	return &CloudCostSettings{TrackedTags: []string(tags)}, nil
}

func trackedCostTags() ([]string, error) {
	var rows []cloudCostSettingsRow
	if err := storage.GetDb().Where("id = 1").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []string{}, nil
	}
	return orEmpty(rows[0].TrackedTags), nil
}

// costSettingsFrom checks the cost use's settings for connection c (nil without the use).
func (s *Service) costSettingsFrom(c *CloudConnection, requested *spec.CostSettings) (*spec.CostSettings, error) {
	if !slices.Contains(c.ScanTypes, spec.UseCost) {
		return nil, nil
	}
	if requested == nil {
		requested = &spec.CostSettings{}
	}
	settings := &spec.CostSettings{
		Source: orDefault(
			requested.Source,
			spec.CostSourceAuto,
		),
		Scope:        orDefault(requested.Scope, spec.CostScopeAccount),
		RefreshHours: requested.RefreshHours,
	}
	switch settings.Scope {
	case spec.CostScopeAccount, spec.CostScopeBilling:
	case spec.CostScopeVia:
		via, err := uuid.Parse(requested.ViaConnectionID)
		if err != nil {
			return nil, errors.New("pick the connection that reads this one's cost")
		}
		if err := s.checkCostVia(c, via); err != nil {
			return nil, err
		}
		settings.ViaConnectionID = via.String()
		return settings, nil
	default:
		return nil, errors.New("cost scope must be account, billing or via")
	}
	switch settings.Source {
	case spec.CostSourceAuto, spec.CostSourceExport, spec.CostSourceAPI:
	default:
		return nil, errors.New("cost source must be auto, export or api")
	}
	if settings.RefreshHours == 0 {
		settings.RefreshHours = spec.DefaultCostRefreshHours
	}
	if settings.RefreshHours < spec.MinCostRefreshHours || settings.RefreshHours > spec.MaxCostRefreshHours {
		return nil, fmt.Errorf("cost refresh must be every %d to %d hours", spec.MinCostRefreshHours,
			spec.MaxCostRefreshHours)
	}
	if settings.Source == spec.CostSourceAPI {
		return settings, nil
	}
	export, err := costExportFrom(c.Provider, requested.Export)
	if err != nil {
		return nil, err
	}
	if export == nil && settings.Source == spec.CostSourceExport {
		return nil, errors.New("export only needs the export's location")
	}
	settings.Export = export
	return settings, nil
}

// costExportFrom checks where the FOCUS export lands; nil when nothing is given.
func costExportFrom(provider string, requested *spec.CostExport) (*spec.CostExport, error) {
	if requested == nil {
		return nil, nil
	}
	export := &spec.CostExport{Prefix: strings.Trim(strings.TrimSpace(requested.Prefix), "/")}
	if !exportPrefixPattern.MatchString(export.Prefix) {
		return nil, errors.New("the export prefix may use letters, digits and !_.*'()/=- only")
	}
	switch provider {
	case spec.ProviderAWS:
		export.Bucket = strings.TrimSpace(requested.Bucket)
		export.Region = strings.ToLower(strings.TrimSpace(requested.Region))
		if export.Bucket == "" {
			return nil, nil
		}
		if !s3BucketPattern.MatchString(export.Bucket) {
			return nil, errors.New("an S3 bucket name is 3 to 63 lowercase letters, digits, dots or hyphens")
		}
		if export.Region == "" {
			export.Region = "us-east-1"
		}
		if !awsRegionPattern.MatchString(export.Region) {
			return nil, errors.New("the bucket's region looks like us-east-1")
		}
	case spec.ProviderAzure:
		export.StorageAccount = strings.ToLower(strings.TrimSpace(requested.StorageAccount))
		export.Container = strings.ToLower(strings.TrimSpace(requested.Container))
		if export.StorageAccount == "" && export.Container == "" {
			return nil, nil
		}
		if !storageAccountPattern.MatchString(export.StorageAccount) {
			return nil, errors.New("a storage account name is 3 to 24 lowercase letters or digits")
		}
		if !containerPattern.MatchString(export.Container) {
			return nil, errors.New("a container name is 3 to 63 lowercase letters, digits or hyphens")
		}
	}
	return export, nil
}

// checkCostVia makes sure the connection named to read c's cost is another one, of the same
// provider, that reads billing for its members itself.
func (s *Service) checkCostVia(c *CloudConnection, via uuid.UUID) error {
	if via == c.ID {
		return errors.New("a connection can't read its own cost via itself")
	}
	other, err := s.findCloudConnection(via)
	if err != nil {
		return errors.New("the connection named for cost doesn't exist")
	}
	settings := other.Cost.Settings
	if other.Provider != c.Provider || settings == nil || settings.Scope != spec.CostScopeBilling {
		return fmt.Errorf("%q doesn't read billing for its members: give it the cost use with the billing scope first",
			other.Name)
	}
	return nil
}

// costReaders names the connections that read their cost via id.
func costReaders(id uuid.UUID) ([]string, error) {
	var names []string
	err := storage.GetDb().Model(&CloudConnection{}).
		Where("cost_settings->>'viaConnectionId' = ?", id.String()).Order("lower(name)").Pluck("name", &names).Error
	return names, err
}

// withTrackedTags gives the probe the control plane's tracked tags with a connection's cost settings.
func withTrackedTags(connection spec.CloudConnection, tags []string) spec.CloudConnection {
	if connection.Cost == nil {
		return connection
	}
	cost := *connection.Cost
	cost.TrackedTags = append([]string{}, tags...)
	connection.Cost = &cost
	return connection
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
