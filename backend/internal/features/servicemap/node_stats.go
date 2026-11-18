package servicemap

import (
	"strings"

	spans_core "obzly/internal/features/spans/core"
)

const (
	nodeTypeService   = "service"
	nodeTypeDatabase  = "database"
	nodeTypeCache     = "cache"
	nodeTypeMessaging = "messaging"
	nodeTypeExternal  = "external"

	healthOK       = "ok"
	healthDegraded = "degraded"
	healthFailing  = "failing"

	// Health thresholds, the same bands the map colours edges by: 5%+ errors
	// is failing; 1%+ errors or a p95 of 800 ms+ is degraded.
	failingErrorRate  = 0.05
	degradedErrorRate = 0.01
	degradedP95Ms     = 800.0
)

// cacheDBSystems are db_system values that are caches rather than
// databases (OTel semantic-convention names, lowercase).
var cacheDBSystems = map[string]bool{
	"redis":     true,
	"valkey":    true,
	"memcached": true,
	"hazelcast": true,
	"coherence": true,
	"geode":     true,
}

// nodeStat is one node's type and RED numbers before collapsing.
type nodeStat struct {
	nodeType string
	requests int64
	errors   int64
	p95Ms    *float64
}

// buildNodeStats keys RED and type by node name. A name with spans of its
// own is a service, whatever its callers' client spans say about it.
func buildNodeStats(stats *spans_core.ServiceNodeStats) map[string]nodeStat {
	result := make(map[string]nodeStat)
	if stats == nil {
		return result
	}
	for _, service := range stats.Services {
		result[service.ServiceName] = nodeStat{
			nodeType: nodeTypeService,
			requests: service.Requests,
			errors:   service.Errors,
			p95Ms:    service.P95Ms,
		}
	}
	for _, peer := range stats.Peers {
		if _, isService := result[peer.Peer]; isService {
			continue
		}
		result[peer.Peer] = nodeStat{
			nodeType: peerType(peer.DBSystem, peer.MessagingSystem),
			requests: peer.Requests,
			errors:   peer.Errors,
			p95Ms:    peer.P95Ms,
		}
	}
	return result
}

// peerCallEdges turns untraced peer calls into span call edges, so they
// merge, rank and collapse like any other.
func peerCallEdges(stats *spans_core.ServiceNodeStats) []spans_core.ServiceCallEdge {
	if stats == nil {
		return nil
	}
	edges := make([]spans_core.ServiceCallEdge, 0, len(stats.PeerEdges))
	for _, peer := range stats.PeerEdges {
		edges = append(edges, spans_core.ServiceCallEdge{
			SrcService:    peer.SrcService,
			DstService:    peer.Peer,
			CallCount:     peer.CallCount,
			ErrorCount:    peer.ErrorCount,
			AvgDurationMs: peer.AvgDurationMs,
		})
	}
	return edges
}

func peerType(dbSystem, messagingSystem string) string {
	switch {
	case cacheDBSystems[strings.ToLower(dbSystem)]:
		return nodeTypeCache
	case dbSystem != "":
		return nodeTypeDatabase
	case messagingSystem != "":
		return nodeTypeMessaging
	default:
		return nodeTypeExternal
	}
}

// collapseNodeStats sums the stats of graph nodes folded into "other"; its
// p95 is unknown (percentiles don't add) and it has no type. Services with no
// edge aren't on the map, so they are skipped.
func collapseNodeStats(
	stats map[string]nodeStat,
	graphNodes map[string]map[string]bool,
	keep map[string]bool,
) map[string]nodeStat {
	final := make(map[string]nodeStat, len(stats))
	for name, stat := range stats {
		if _, onMap := graphNodes[name]; !onMap {
			continue
		}
		if keep[name] {
			final[name] = stat
			continue
		}
		other := final[otherNodeName]
		other.requests += stat.requests
		other.errors += stat.errors
		final[otherNodeName] = other
	}
	return final
}

// applyNodeStat fills dto's type, RED and health from stat.
func applyNodeStat(dto *ServiceMapNodeDTO, stat nodeStat) {
	dto.Type = stat.nodeType
	requests, errors := stat.requests, stat.errors
	dto.Requests = &requests
	dto.Errors = &errors
	if requests == 0 {
		return
	}
	errorRate := float64(errors) / float64(requests)
	dto.ErrorRate = &errorRate
	dto.P95LatencyMs = stat.p95Ms
	// "other" is a bucket of services, not one with a health of its own.
	if stat.nodeType != "" {
		dto.Health = healthOf(errorRate, stat.p95Ms)
	}
}

func healthOf(errorRate float64, p95Ms *float64) string {
	switch {
	case errorRate >= failingErrorRate:
		return healthFailing
	case errorRate >= degradedErrorRate, p95Ms != nil && *p95Ms >= degradedP95Ms:
		return healthDegraded
	default:
		return healthOK
	}
}
