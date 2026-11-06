package synthetics

import (
	"fmt"
	"strings"

	"obzly/internal/features/sharedconfig"

	"github.com/google/uuid"
)

// MonitorAlertsFor is siteID's monitor alert rules: Down, plus Degraded and certificate when asked.
// Down is muted while a parent in the same project is down, so an outage notifies once, for its cause.
func (s *Service) MonitorAlertsFor(siteID string) ([]sharedconfig.MonitorAlert, error) {
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	locations, err := s.locationCount()
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*Check{}
	for _, check := range checks {
		byID[check.ID] = check
	}
	alerts := []sharedconfig.MonitorAlert{}
	for _, check := range checks {
		destination, ok := destinations[check.DestinationID]
		if !ok || destination.SiteID != siteID || !check.Enabled || len(check.Alerts.ChannelIDs) == 0 {
			continue
		}
		failing := failingQuery(check)
		down := downThreshold(check, locations)
		var muted []string
		for _, parentID := range check.ParentIDs {
			parent, ok := byID[parentID]
			if !ok || !parent.Enabled || destinations[parent.DestinationID] == nil ||
				destinations[parent.DestinationID].ProjectID != destination.ProjectID {
				continue
			}
			muted = append(muted, fmt.Sprintf("(%s >= %d)", failingQuery(parent), downThreshold(parent, locations)))
		}
		unlessParentDown := func(query string) string {
			if len(muted) == 0 {
				return query
			}
			return fmt.Sprintf("(%s) unless on() (%s)", query, strings.Join(muted, " or on() "))
		}
		downQuery := unlessParentDown(failing)
		alert := func(kind, name, query, comparison string, threshold float64, severity, summary string) {
			alerts = append(alerts, sharedconfig.MonitorAlert{
				Key: "synthetics:" + check.ID.String() + ":" + kind, ProjectID: destination.ProjectID,
				Name: name, PromQL: query, Comparison: comparison, Threshold: threshold,
				IntervalSeconds: max(60, min(check.IntervalSeconds, 300)), RepeatSeconds: check.Alerts.RepeatSeconds,
				Severity: severity, Summary: summary, ChannelIDs: check.Alerts.ChannelIDs,
				CheckID: check.ID.String(),
			})
		}
		alert("down", "Synthetics: "+check.Name+" is down", downQuery, ">=", float64(down), "critical",
			fmt.Sprintf("%s fails from at least %d location(s)", check.Name, down))
		if check.Alerts.Notify == "degraded" {
			window := windowSeconds(check)
			degraded := unlessParentDown(fmt.Sprintf(
				`(count(min by (location) (last_over_time(synthetics_up{check_id=%q}[%ds])) == 0 `+
					`or max by (location) (last_over_time(synthetics_warn{check_id=%q}[%ds])) == 1) >= 1) `+
					`unless on() (%s >= %d)`,
				check.ID.String(), window, check.ID.String(), window, failing, down))
			alert("degraded", "Synthetics: "+check.Name+" is degraded", degraded, ">=", 1, "warning",
				check.Name+" fails from some locations or breaches a warning")
		}
		if check.Alerts.CertDays > 0 {
			cert := fmt.Sprintf(`min(last_over_time(synthetics_cert_expiry_seconds{check_id=%q}[%ds])) / 86400`,
				check.ID.String(), max(windowSeconds(check), 7200))
			alert("cert", "Synthetics: "+check.Name+" certificate expiring", cert, "<",
				float64(check.Alerts.CertDays), "warning",
				fmt.Sprintf("%s's certificate expires within %d days", check.Name, check.Alerts.CertDays))
		}
	}
	return alerts, nil
}

// failingQuery counts the locations whose latest run of check failed. A
// location can have several series (older probes labelled runs differently),
// so they are grouped by location rather than counted one by one.
func failingQuery(check *Check) string {
	return fmt.Sprintf(`count(min by (location) (last_over_time(synthetics_up{check_id=%q}[%ds])) == 0)`,
		check.ID.String(), windowSeconds(check))
}

// windowSeconds reaches back over two runs and a minute of slack, so a
// location's latest result is always in it.
func windowSeconds(check *Check) int {
	return 2*check.IntervalSeconds + 60
}

// downThreshold: red from DownLocations failing, else half of the
// monitor's locations, at least one (as the UI's status).
func downThreshold(check *Check, allLocations int) int {
	if check.DownLocations > 0 {
		return check.DownLocations
	}
	locations := len(check.Locations)
	if locations == 0 {
		locations = allLocations
	}
	return max(1, (locations+1)/2)
}

func (s *Service) locationCount() (int, error) {
	probes, err := s.repository.probes()
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, probe := range probes {
		seen[probe.Location] = true
	}
	return len(seen), nil
}
