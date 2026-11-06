package synthetics

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"obzly/internal/features/sharedconfig"
	"obzly/internal/synthetics/spec"

	"github.com/google/uuid"
)

const maxParents = 10

// checkParents validates a monitor's parents: existing monitors, not
// itself, and no cycle through them.
func (s *Service) checkParents(id uuid.UUID, parentIDs []uuid.UUID) (UUIDs, error) {
	if len(parentIDs) == 0 {
		return UUIDs{}, nil
	}
	if len(parentIDs) > maxParents {
		return nil, fmt.Errorf("a monitor can depend on at most %d others", maxParents)
	}
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	graph := map[uuid.UUID][]uuid.UUID{}
	for _, check := range checks {
		graph[check.ID] = check.ParentIDs
	}
	var parents UUIDs
	for _, parentID := range parentIDs {
		if parentID == id {
			return nil, errors.New("a monitor can't depend on itself")
		}
		if _, ok := graph[parentID]; !ok {
			return nil, errors.New("a parent monitor doesn't exist")
		}
		if !slices.Contains(parents, parentID) {
			parents = append(parents, parentID)
		}
	}
	if id != uuid.Nil {
		graph[id] = parents
		if path := cycleThrough(graph, id); path != nil {
			names := map[uuid.UUID]string{}
			for _, check := range checks {
				names[check.ID] = check.Name
			}
			var labels []string
			for _, node := range path {
				labels = append(labels, names[node])
			}
			return nil, fmt.Errorf("these dependencies would make a loop: %s", strings.Join(labels, " → "))
		}
	}
	return parents, nil
}

// cycleThrough is a dependency path from start back to start, if any.
func cycleThrough(graph map[uuid.UUID][]uuid.UUID, start uuid.UUID) []uuid.UUID {
	visited := map[uuid.UUID]bool{}
	var walk func(node uuid.UUID, path []uuid.UUID) []uuid.UUID
	walk = func(node uuid.UUID, path []uuid.UUID) []uuid.UUID {
		for _, parent := range graph[node] {
			if parent == start {
				return append(path, parent)
			}
			if visited[parent] {
				continue
			}
			visited[parent] = true
			if found := walk(parent, append(path, parent)); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(start, []uuid.UUID{start})
}

// checkAlerts validates a monitor's alert settings against the shared
// channels (Obzly settings).
func checkAlerts(normalized *spec.MonitorAlerts, request *CheckAlerts) (CheckAlerts, error) {
	if request == nil || len(request.ChannelIDs) == 0 {
		return CheckAlerts{ChannelIDs: []uuid.UUID{}}, nil
	}
	channels, err := sharedconfig.SharedChannels()
	if err != nil {
		return CheckAlerts{}, err
	}
	known := map[uuid.UUID]bool{}
	for _, channel := range channels {
		known[channel.ID] = true
	}
	alerts := CheckAlerts{
		Notify: normalized.Notify, CertDays: normalized.CertDays, RepeatSeconds: normalized.RepeatSeconds,
	}
	for _, id := range request.ChannelIDs {
		if !known[id] {
			return CheckAlerts{}, errors.New("an alert channel doesn't exist in Obzly settings")
		}
		if !slices.Contains(alerts.ChannelIDs, id) {
			alerts.ChannelIDs = append(alerts.ChannelIDs, id)
		}
	}
	return alerts, nil
}
