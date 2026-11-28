package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"obzly/internal/authz"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

const (
	quietAfter   = 5 * time.Minute
	stoppedAfter = 24 * time.Hour
	rateWindow   = 15 * time.Minute
	maxSources   = 200
	// statusCacheTTL: the page refreshes every 30 s; a status this fresh is
	// served from memory.
	statusCacheTTL = 30 * time.Second
)

var ErrForbidden = errors.New("insufficient permissions to view this project's ingestion status")

var signalOrder = map[string]int{SignalLogs: 0, SignalTraces: 1, SignalMetrics: 2}

var healthOrder = map[string]int{HealthSending: 0, HealthQuiet: 1, HealthStopped: 2}

type Service struct {
	repo  *repository
	now   func() time.Time
	cache *statusCache
}

type statusCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[uuid.UUID]*IngestionStatusDTO
}

// GetStatus lists the sources sending to a project (seen in the last
// sourceRetention), most active first. Any project role may read it.
func (s *Service) GetStatus(
	ctx context.Context,
	user *users_models.User,
	projectID uuid.UUID,
) (*IngestionStatusDTO, error) {
	decision, err := authz.Authorize(ctx, user, authz.ProjectRead, authz.Project(projectID))
	if err != nil {
		return nil, fmt.Errorf("failed to verify project access: %w", err)
	}
	if !decision.Allowed {
		return nil, ErrForbidden
	}
	now := s.now()
	if cached := s.cache.get(projectID, now); cached != nil {
		return cached, nil
	}
	status, err := s.build(ctx, projectID, now)
	if err != nil {
		return nil, err
	}
	s.cache.put(projectID, status)
	return status, nil
}

func (s *Service) build(ctx context.Context, projectID uuid.UUID, now time.Time) (*IngestionStatusDTO, error) {
	rows, err := s.repo.listSources(ctx, projectID, now.Add(-sourceRetention))
	if err != nil {
		return nil, err
	}
	windowStart := now.Add(-rateWindow).Truncate(time.Minute)
	counts, err := s.repo.sumCounts(ctx, projectID, windowStart)
	if err != nil {
		return nil, err
	}
	refusals, err := s.repo.sumRefusals(ctx, projectID, now.Add(-refusedWindow))
	if err != nil {
		return nil, err
	}
	recent := make(map[[2]string]int64, len(counts))
	for _, row := range counts {
		recent[[2]string{row.SourceKey, row.Signal}] = row.Count
	}

	bySource := map[string]*SourceStatusDTO{}
	// attributes: each signal's row may know different descriptive values
	// (a pod's logs lack the service name its spans carry); the newest wins.
	attributes := map[string]map[string]string{}
	ranks := map[string]int{}
	var order []string
	for _, row := range rows {
		source, ok := bySource[row.SourceKey]
		if !ok {
			source = &SourceStatusDTO{ID: row.SourceKey, IdentifiedBy: row.IdentifiedBy}
			bySource[row.SourceKey] = source
			attributes[row.SourceKey] = map[string]string{}
			order = append(order, row.SourceKey)
		}
		var rowAttributes map[string]string
		_ = json.Unmarshal(row.Attributes, &rowAttributes)
		for name, value := range rowAttributes {
			if _, known := attributes[row.SourceKey][name]; !known {
				attributes[row.SourceKey][name] = value
			}
		}
		if row.DataSourceRank > ranks[row.SourceKey] {
			ranks[row.SourceKey] = row.DataSourceRank
			source.DataSource = row.DataSource
		}
		count := recent[[2]string{row.SourceKey, row.Signal}]
		source.Signals = append(source.Signals, SignalStatusDTO{
			Signal:      row.Signal,
			Health:      healthAt(row.LastSeen, now),
			FirstSeen:   row.FirstSeen.UTC(),
			LastSeen:    row.LastSeen.UTC(),
			RecentCount: count,
			PerMinute:   perMinute(count, row.FirstSeen, windowStart, now),
		})
	}

	sources := make([]SourceStatusDTO, 0, len(order))
	totals := map[string]float64{}
	for _, key := range order {
		source := bySource[key]
		describe(source, attributes[key])
		sort.Slice(source.Signals, func(i, j int) bool {
			return signalOrder[source.Signals[i].Signal] < signalOrder[source.Signals[j].Signal]
		})
		for _, signal := range source.Signals {
			if source.FirstSeen.IsZero() || signal.FirstSeen.Before(source.FirstSeen) {
				source.FirstSeen = signal.FirstSeen
			}
			if signal.LastSeen.After(source.LastSeen) {
				source.LastSeen = signal.LastSeen
			}
			totals[key] += signal.PerMinute
		}
		source.Health = healthAt(source.LastSeen, now)
		sources = append(sources, *source)
	}
	sort.SliceStable(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if healthOrder[a.Health] != healthOrder[b.Health] {
			return healthOrder[a.Health] < healthOrder[b.Health]
		}
		if totals[a.ID] != totals[b.ID] {
			return totals[a.ID] > totals[b.ID]
		}
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return a.Name < b.Name
	})
	truncated := len(rows) >= maxSourceRows
	if len(sources) > maxSources {
		sources = sources[:maxSources]
		truncated = true
	}
	return &IngestionStatusDTO{
		Sources:            sources,
		Truncated:          truncated,
		GeneratedAt:        now,
		QuietAfterMinutes:  int(quietAfter / time.Minute),
		StoppedAfterHours:  int(stoppedAfter / time.Hour),
		RateWindowMinutes:  int(rateWindow / time.Minute),
		Refused:            refusedDTOs(refusals),
		RefusedWindowHours: int(refusedWindow / time.Hour),
	}, nil
}

func (c *statusCache) get(projectID uuid.UUID, now time.Time) *IngestionStatusDTO {
	if c.ttl <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	status, ok := c.entries[projectID]
	if !ok || now.Sub(status.GeneratedAt) >= c.ttl {
		return nil
	}
	return status
}

func (c *statusCache) put(projectID uuid.UUID, status *IngestionStatusDTO) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cached := range c.entries {
		if status.GeneratedAt.Sub(cached.GeneratedAt) >= c.ttl {
			delete(c.entries, id)
		}
	}
	c.entries[projectID] = status
}

// describe sets a source's location and display name from its attributes.
func describe(source *SourceStatusDTO, attributes map[string]string) {
	location := SourceLocationDTO{
		Host:             attributes[attrHostName],
		Cluster:          attributes[attrCluster],
		Namespace:        attributes[attrNamespace],
		WorkloadKind:     attributes[attrWorkloadKind],
		Workload:         attributes[attrWorkload],
		ServiceName:      attributes[attrServiceName],
		ServiceNamespace: attributes[attrServiceNamespace],
		SDKLanguage:      attributes[attrSDKLanguage],
	}
	name := "Unidentified source"
	switch source.IdentifiedBy {
	case IdentifiedByExplicit:
		name = attributes[attrSource]
	case IdentifiedByWorkload:
		name = location.Workload
	case IdentifiedByService:
		name = location.ServiceName
	case IdentifiedByCluster:
		name = location.Cluster
	case IdentifiedByHost:
		name = location.Host
	}
	source.Name, source.Location = name, location
}

func healthAt(lastSeen, now time.Time) string {
	switch age := now.Sub(lastSeen); {
	case age < quietAfter:
		return HealthSending
	case age < stoppedAfter:
		return HealthQuiet
	default:
		return HealthStopped
	}
}

// perMinute averages count over the rate window, or over the time since
// firstSeen when the source started inside it, so a new source isn't
// diluted by minutes before it existed.
func perMinute(count int64, firstSeen, windowStart, now time.Time) float64 {
	if count == 0 {
		return 0
	}
	from := windowStart
	if firstSeen.After(from) {
		from = firstSeen.Truncate(time.Minute)
	}
	minutes := math.Max(1, now.Sub(from).Minutes())
	return math.Round(float64(count)/minutes*10) / 10
}
