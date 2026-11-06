package synthetics

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"obzly/internal/features/sharedconfig"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/synthetics/spec"
)

const maxImportBytes = 2 << 20

// importPlan is what applying a file would write.
type importPlan struct {
	create, update []*Check
	deleteIDs      []uuid.UUID
	windows        maintenancePlan
	preview        ImportPreview
}

// PreviewImport reports what importing the file would do; nothing changes.
func (s *Service) PreviewImport(user *users_models.User, request ImportRequest) (*ImportPreview, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	plan, err := s.planImport(request)
	if err != nil {
		return nil, err
	}
	return &plan.preview, nil
}

// ApplyImport applies the whole file or, when anything in it is wrong,
// none of it.
func (s *Service) ApplyImport(user *users_models.User, request ImportRequest) (*ImportPreview, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	plan, err := s.planImport(request)
	if err != nil {
		return nil, err
	}
	if len(plan.preview.Errors) > 0 {
		return &plan.preview, nil
	}
	for _, check := range plan.create {
		check.CreatedBy = &user.ID
	}
	for _, window := range plan.windows.create {
		window.CreatedBy = &user.ID
	}
	if err := s.repository.applyImport(plan.create, plan.update, plan.deleteIDs, plan.windows); err != nil {
		return nil, err
	}
	plan.preview.Applied = true
	s.audit(user, fmt.Sprintf("Synthetic monitors imported: %d created, %d updated, %d deleted",
		len(plan.create), len(plan.update), len(plan.deleteIDs)))
	if windows := plan.windows; len(windows.create)+len(windows.update)+len(windows.deleteIDs) > 0 {
		s.audit(user, fmt.Sprintf("Synthetics maintenance windows imported: %d created, %d updated, %d deleted",
			len(windows.create), len(windows.update), len(windows.deleteIDs)))
	}
	return &plan.preview, nil
}

// Export writes the chosen monitors (all when ids is empty) as YAML, with
// placeholders and no secret values.
func (s *Service) Export(ids []uuid.UUID) ([]byte, error) {
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	checkNames := map[uuid.UUID]string{}
	for _, check := range checks {
		checkNames[check.ID] = check.Name
	}
	channelNames, err := channelNamesByID()
	if err != nil {
		return nil, err
	}
	var monitors []spec.Monitor
	for _, check := range checks {
		if len(ids) > 0 && !slices.Contains(ids, check.ID) {
			continue
		}
		name := ""
		if destination, ok := destinations[check.DestinationID]; ok {
			name = destination.Name
		}
		monitors = append(monitors, check.monitor(name, checkNames, channelNames))
	}
	if len(ids) > 0 {
		// A selection leaves windows out: they may name monitors not in it.
		return spec.MarshalMonitorList(monitors)
	}
	maintenance, err := s.exportMaintenance(checkNames)
	if err != nil {
		return nil, err
	}
	return spec.MarshalMonitorFile(monitors, maintenance)
}

func (s *Service) planImport(request ImportRequest) (*importPlan, error) {
	plan := &importPlan{preview: ImportPreview{
		Create: []string{}, Update: []ImportChange{}, Unchanged: []string{}, Delete: []string{},
		Secrets: []SecretState{}, Errors: []spec.ImportError{},
		Maintenance: MaintenancePreview{
			Create: []string{}, Update: []ImportChange{}, Unchanged: []string{}, Delete: []string{},
		},
	}}
	if len(request.YAML) > maxImportBytes {
		plan.preview.Errors = append(plan.preview.Errors, spec.ImportError{Message: "the file is larger than 2 MB"})
		return plan, nil
	}
	list, problems := spec.ParseMonitorList([]byte(request.YAML))
	if len(problems) > 0 {
		plan.preview.Errors = problems
		return plan, nil
	}

	destinations, err := s.repository.destinations()
	if err != nil {
		return nil, err
	}
	destinationsByName := map[string]*Destination{}
	for _, destination := range destinations {
		destinationsByName[strings.ToLower(destination.Name)] = destination
	}
	existing, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	existingByName := map[string]*Check{}
	for _, check := range existing {
		existingByName[strings.ToLower(check.Name)] = check
	}

	// Every monitor's id up front, so a parent can come later in the file.
	ids := map[string]uuid.UUID{}
	for _, check := range existing {
		ids[strings.ToLower(check.Name)] = check.ID
	}
	for _, monitor := range list.Monitors {
		if _, ok := ids[strings.ToLower(monitor.Name)]; !ok {
			ids[strings.ToLower(monitor.Name)] = uuid.New()
		}
	}
	channelIDs, err := channelIDsByName()
	if err != nil {
		return nil, err
	}
	graph := map[uuid.UUID][]uuid.UUID{}
	for _, check := range existing {
		graph[check.ID] = check.ParentIDs
	}

	inFile := map[string]bool{}
	secretStates := map[string]bool{}
	for _, monitor := range list.Monitors {
		inFile[strings.ToLower(monitor.Name)] = true
		destination, problem := pickDestination(monitor.Destination, destinations, destinationsByName)
		if problem != "" {
			plan.preview.Errors = append(plan.preview.Errors, spec.ImportError{Monitor: monitor.Name, Message: problem})
			continue
		}
		previous := existingByName[strings.ToLower(monitor.Name)]
		check, stored, err := s.importedCheck(monitor, destination, previous, request.Secrets)
		if err != nil {
			return nil, err
		}
		if problem := resolveNames(check, monitor, ids, channelIDs); problem != "" {
			plan.preview.Errors = append(plan.preview.Errors, spec.ImportError{Monitor: monitor.Name, Message: problem})
			continue
		}
		graph[ids[strings.ToLower(monitor.Name)]] = check.ParentIDs
		for _, name := range check.Definition.placeholders() {
			secretStates[name] = secretStates[name] || stored[name]
		}
		if previous == nil {
			check.ID, check.Version, check.CreatedAt, check.UpdatedAt = ids[strings.ToLower(monitor.Name)], 1, s.now(), s.now()
			plan.create = append(plan.create, check)
			plan.preview.Create = append(plan.preview.Create, check.Name)
		} else {
			changes := describeChanges(previous, check, request.Secrets)
			if len(changes) == 0 {
				plan.preview.Unchanged = append(plan.preview.Unchanged, check.Name)
				continue
			}
			check.ID, check.Version, check.CreatedAt, check.CreatedBy = previous.ID, previous.Version+1, previous.CreatedAt, previous.CreatedBy
			check.UpdatedAt = s.now()
			plan.update = append(plan.update, check)
			plan.preview.Update = append(plan.preview.Update, ImportChange{Name: check.Name, Changes: changes})
		}
	}
	for _, monitor := range list.Monitors {
		if path := cycleThrough(graph, ids[strings.ToLower(monitor.Name)]); path != nil {
			plan.preview.Errors = append(plan.preview.Errors, spec.ImportError{
				Monitor: monitor.Name, Message: "its depends_on makes a loop",
			})
		}
	}
	pruned := map[string]bool{}
	if list.Prune {
		for _, check := range existing {
			if !inFile[strings.ToLower(check.Name)] {
				plan.deleteIDs = append(plan.deleteIDs, check.ID)
				plan.preview.Delete = append(plan.preview.Delete, check.Name)
				pruned[strings.ToLower(check.Name)] = true
			}
		}
	}
	if err := s.planMaintenance(list, ids, pruned, plan); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(secretStates) {
		plan.preview.Secrets = append(plan.preview.Secrets, SecretState{Name: name, Stored: secretStates[name]})
	}
	return plan, nil
}

// importedCheck builds the stored form of monitor, secrets from the request else from previous;
// stored reports which placeholders end up with a value.
func (s *Service) importedCheck(
	monitor spec.Monitor, destination *Destination, previous *Check, provided map[string]string,
) (*Check, map[string]bool, error) {
	values := map[string]string{}
	if previous != nil {
		existing, err := s.decryptSecrets(previous.SecretsEncrypted)
		if err != nil {
			return nil, nil, err
		}
		values = existing
	}
	placeholders := monitor.Definition.Placeholders()
	stored := map[string]bool{}
	for _, name := range placeholders {
		if value := provided[name]; value != "" {
			values[name] = value
		}
		_, stored[name] = values[name]
	}
	encrypted := ""
	if previous != nil && !secretsChanged(placeholders, provided) && sameKeys(values, placeholders) {
		// Unchanged secrets keep their ciphertext, so the preview sees no change.
		encrypted = previous.SecretsEncrypted
	} else {
		var err error
		if encrypted, err = s.encryptSecrets(values, placeholders); err != nil {
			return nil, nil, err
		}
	}
	return &Check{
		Name: monitor.Name, Type: monitor.Definition.Type, Enabled: monitor.Enabled,
		IntervalSeconds: monitor.IntervalSeconds, DownLocations: monitor.DownLocations,
		Locations: monitor.Locations, DestinationID: destination.ID,
		Definition: DefinitionJSON(monitor.Definition), SecretsEncrypted: encrypted, Folder: monitor.Folder,
	}, stored, nil
}

// resolveNames sets check's parents and alert channels from monitor's
// names; a problem is a name that doesn't exist.
func resolveNames(check *Check, monitor spec.Monitor, ids, channelIDs map[string]uuid.UUID) string {
	check.ParentIDs = UUIDs{}
	for _, parent := range monitor.Parents {
		id, ok := ids[strings.ToLower(parent)]
		if !ok {
			return fmt.Sprintf("depends_on: no monitor named %q", parent)
		}
		check.ParentIDs = append(check.ParentIDs, id)
	}
	check.Alerts = CheckAlerts{ChannelIDs: []uuid.UUID{}}
	if monitor.Alerts != nil && len(monitor.Alerts.Channels) > 0 {
		check.Alerts.Notify, check.Alerts.CertDays = monitor.Alerts.Notify, monitor.Alerts.CertDays
		check.Alerts.RepeatSeconds = monitor.Alerts.RepeatSeconds
		for _, channel := range monitor.Alerts.Channels {
			id, ok := channelIDs[strings.ToLower(channel)]
			if !ok {
				return fmt.Sprintf("alerts: no channel named %q in Obzly settings", channel)
			}
			check.Alerts.ChannelIDs = append(check.Alerts.ChannelIDs, id)
		}
	}
	return ""
}

func normalizedAlerts(a CheckAlerts) CheckAlerts {
	if len(a.ChannelIDs) == 0 {
		return CheckAlerts{}
	}
	return a
}

func channelNamesByID() (map[uuid.UUID]string, error) {
	channels, err := sharedconfig.SharedChannels()
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	for _, channel := range channels {
		names[channel.ID] = channel.Name
	}
	return names, nil
}

func channelIDsByName() (map[string]uuid.UUID, error) {
	channels, err := sharedconfig.SharedChannels()
	if err != nil {
		return nil, err
	}
	ids := map[string]uuid.UUID{}
	for _, channel := range channels {
		ids[strings.ToLower(channel.Name)] = channel.ID
	}
	return ids, nil
}

func pickDestination(name string, all []*Destination, byName map[string]*Destination) (*Destination, string) {
	if name == "" {
		if len(all) == 1 {
			return all[0], ""
		}
		return nil, "destination is required (there isn't exactly one to default to)"
	}
	if destination, ok := byName[strings.ToLower(name)]; ok {
		return destination, ""
	}
	return nil, fmt.Sprintf("destination %q doesn't exist; create it under Synthetics → Destinations first", name)
}

// describeChanges lists what differs between the stored monitor and the
// imported one, for the preview.
func describeChanges(old, updated *Check, provided map[string]string) []string {
	var changes []string
	if old.Name != updated.Name {
		changes = append(changes, fmt.Sprintf("renamed %q → %q", old.Name, updated.Name))
	}
	if old.Enabled != updated.Enabled {
		changes = append(changes, map[bool]string{true: "enabled", false: "disabled"}[updated.Enabled])
	}
	if old.IntervalSeconds != updated.IntervalSeconds {
		changes = append(changes, fmt.Sprintf("interval %ds → %ds", old.IntervalSeconds, updated.IntervalSeconds))
	}
	if old.DownLocations != updated.DownLocations {
		changes = append(changes, fmt.Sprintf("down_locations %d → %d", old.DownLocations, updated.DownLocations))
	}
	if !slices.Equal(old.Locations, updated.Locations) {
		changes = append(
			changes,
			fmt.Sprintf("locations %s → %s", locationsText(old.Locations), locationsText(updated.Locations)),
		)
	}
	if old.DestinationID != updated.DestinationID {
		changes = append(changes, "destination changed")
	}
	before, after := spec.Definition(old.Definition), spec.Definition(updated.Definition)
	if before.Type != after.Type {
		changes = append(changes, fmt.Sprintf("type %s → %s", before.Type, after.Type))
	}
	if before.TimeoutSeconds != after.TimeoutSeconds {
		changes = append(changes, fmt.Sprintf("timeout %ds → %ds", before.TimeoutSeconds, after.TimeoutSeconds))
	}
	if before.TargetLabel() != after.TargetLabel() {
		changes = append(changes, fmt.Sprintf("target %s → %s", before.TargetLabel(), after.TargetLabel()))
	}
	if !jsonEqual(before.Request, after.Request) && before.TargetLabel() == after.TargetLabel() {
		changes = append(changes, "request changed")
	}
	if !jsonEqual(before.Assertions, after.Assertions) {
		changes = append(changes, fmt.Sprintf("assertions %d → %d", len(before.Assertions), len(after.Assertions)))
	}
	switch {
	case len(before.Steps) != len(after.Steps):
		changes = append(changes, fmt.Sprintf("steps %d → %d", len(before.Steps), len(after.Steps)))
	case !jsonEqual(before.Steps, after.Steps):
		changes = append(changes, "steps changed")
	}
	if !jsonEqual(before.Variables, after.Variables) {
		changes = append(changes, "variables changed")
	}
	if before.KeepsCookies() != after.KeepsCookies() {
		changes = append(changes, map[bool]string{true: "cookies on", false: "cookies off"}[after.KeepsCookies()])
	}
	for _, part := range []struct {
		name          string
		before, after any
	}{{"tls", before.TLS, after.TLS}, {"dns", before.DNS, after.DNS}, {"icmp", before.ICMP, after.ICMP}} {
		if !jsonEqual(part.before, part.after) {
			changes = append(changes, part.name+" options changed")
		}
	}
	if secretsChanged(after.Placeholders(), provided) || old.SecretsEncrypted != updated.SecretsEncrypted {
		changes = append(changes, "secret values updated")
	}
	if old.Folder != updated.Folder {
		changes = append(changes, fmt.Sprintf("folder %q → %q", old.Folder, updated.Folder))
	}
	if !slices.Equal(old.ParentIDs, updated.ParentIDs) {
		changes = append(changes, fmt.Sprintf("depends_on %d → %d", len(old.ParentIDs), len(updated.ParentIDs)))
	}
	if !jsonEqual(normalizedAlerts(old.Alerts), normalizedAlerts(updated.Alerts)) {
		changes = append(changes, "alerts changed")
	}
	return changes
}

func (d DefinitionJSON) placeholders() []string { return spec.Definition(d).Placeholders() }

func secretsChanged(placeholders []string, provided map[string]string) bool {
	for _, name := range placeholders {
		if provided[name] != "" {
			return true
		}
	}
	return false
}

func sameKeys(values map[string]string, placeholders []string) bool {
	for name := range values {
		if !slices.Contains(placeholders, name) {
			return false
		}
	}
	return true
}

func locationsText(locations []string) string {
	if len(locations) == 0 {
		return "all"
	}
	return strings.Join(locations, ", ")
}

// jsonEqual compares as stored, with nil and empty the same.
func jsonEqual(a, b any) bool {
	return emptyAsBlank(a) == emptyAsBlank(b)
}

func emptyAsBlank(value any) string {
	encoded, _ := json.Marshal(value)
	switch text := string(encoded); text {
	case "null", "[]", "{}":
		return ""
	default:
		return text
	}
}
