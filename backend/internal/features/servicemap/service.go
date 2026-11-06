// Package servicemap merges span-derived service-call edges with
// network-derived edges (networkEdges.go) into one graph, computed on every
// read. The network source covers services with no tracing SDK (e.g. a bare
// Postgres or Redis).
package servicemap

import (
	"errors"
	"fmt"
	"sort"
	"time"

	projects_services "obzly/internal/features/projects/services"
	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

const (
	sourceSpans   = "spans"
	sourceNetwork = "network"

	// otherNodeName is the bucket nodes beyond maxRankedNodes collapse into.
	otherNodeName = "other"

	// maxServiceMapLookback: both queries scan every segment overlapping the
	// window, and the queriers impose no max range of their own.
	maxServiceMapLookback = 24 * time.Hour

	// maxRankedNodes keeps the graph cheap and readable; the rest collapse
	// into "other" (see rankAndCollapse).
	maxRankedNodes = 50
)

type ServiceMapService struct {
	projectService *projects_services.ProjectService
}

// GetServiceMap merges span call edges with network-metric edges into one graph.
func (s *ServiceMapService) GetServiceMap(
	projectID uuid.UUID,
	from, to *time.Time,
	user *users_models.User,
) (*ServiceMapResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	resolvedFrom, resolvedTo := clampTimeRange(from, to)
	timeRange := &spans_core.TimeRangeDTO{From: &resolvedFrom, To: &resolvedTo}

	spanEdges, err := spans_core.GetSpanCoreRepository().ServiceCallEdges(projectID, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to load span-derived service edges: %w", err)
	}

	netEdges, netK8sMetadata, err := networkEdges(projectID, resolvedFrom, resolvedTo)
	if err != nil {
		return nil, fmt.Errorf("failed to load network-derived service edges: %w", err)
	}

	spanK8sMetadata, err := spans_core.GetSpanCoreRepository().ServiceK8sMetadata(projectID, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to load span-derived service k8s metadata: %w", err)
	}

	return mergeServiceMap(spanEdges, netEdges, mergeK8sMetadata(spanK8sMetadata, netK8sMetadata)), nil
}

// mergeK8sMetadata prefers spans' values field by field; network metrics
// carry only a namespace, which fills the gap for untraced services.
func mergeK8sMetadata(
	spanMetadata map[string]spans_core.K8sResourceInfo,
	networkMetadata map[string]networkK8sInfo,
) map[string]k8sInfo {
	merged := make(map[string]k8sInfo, len(spanMetadata)+len(networkMetadata))
	for name, info := range spanMetadata {
		merged[name] = k8sInfo{Namespace: info.Namespace, Deployment: info.Deployment, Node: info.Node}
	}
	for name, info := range networkMetadata {
		existing := merged[name]
		if existing.Namespace == "" {
			existing.Namespace = info.Namespace
		}
		merged[name] = existing
	}
	return merged
}

type k8sInfo struct {
	Namespace  string
	Deployment string
	Node       string
}

func (s *ServiceMapService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to query project service map")
	}
	return nil
}

// clampTimeRange defaults "to" to now and pulls "from" forward so the window
// is at most maxServiceMapLookback.
func clampTimeRange(from, to *time.Time) (time.Time, time.Time) {
	resolvedTo := time.Now().UTC()
	if to != nil {
		resolvedTo = *to
	}

	earliestAllowedFrom := resolvedTo.Add(-maxServiceMapLookback)
	resolvedFrom := earliestAllowedFrom
	if from != nil && from.After(earliestAllowedFrom) {
		resolvedFrom = *from
	}

	return resolvedFrom, resolvedTo
}

type edgeAccumulator struct {
	sources    map[string]bool
	hasSpans   bool
	callCount  int64
	errorCount int64
	// durationMsWeighted is Σ(avgDurationMs * callCount), so edges collapsed
	// into "other" average by call count, not as an average of averages.
	durationMsWeighted float64
	hasNetwork         bool
	bytesTotal         int64
}

func mergeServiceMap(
	spanEdges []spans_core.ServiceCallEdge,
	netEdges []NetworkEdge,
	k8sMetadata map[string]k8sInfo,
) *ServiceMapResponseDTO {
	edges := make(map[[2]string]*edgeAccumulator)

	for _, edge := range spanEdges {
		key := [2]string{edge.SrcService, edge.DstService}
		acc := edgeFor(edges, key)
		acc.sources[sourceSpans] = true
		acc.hasSpans = true
		acc.callCount += edge.CallCount
		acc.errorCount += edge.ErrorCount
		acc.durationMsWeighted += edge.AvgDurationMs * float64(edge.CallCount)
	}
	for _, edge := range netEdges {
		key := [2]string{edge.SrcService, edge.DstService}
		acc := edgeFor(edges, key)
		acc.sources[sourceNetwork] = true
		acc.hasNetwork = true
		acc.bytesTotal += edge.BytesTotal
	}

	nodeSources, nodeVolume := collectNodes(edges)
	keep := rankAndCollapse(nodeVolume)

	finalEdges := collapseEdges(edges, keep)
	finalNodeSources := collapseNodeSources(nodeSources, keep)

	return &ServiceMapResponseDTO{
		Nodes: buildNodeDTOs(finalNodeSources, k8sMetadata),
		Edges: buildEdgeDTOs(finalEdges),
	}
}

func edgeFor(edges map[[2]string]*edgeAccumulator, key [2]string) *edgeAccumulator {
	acc, ok := edges[key]
	if !ok {
		acc = &edgeAccumulator{sources: make(map[string]bool)}
		edges[key] = acc
	}
	return acc
}

// collectNodes derives each node's sources and traffic volume: calls for
// span edges plus bytes for network edges, summed over its edges.
func collectNodes(edges map[[2]string]*edgeAccumulator) (map[string]map[string]bool, map[string]float64) {
	sources := make(map[string]map[string]bool)
	volume := make(map[string]float64)

	touch := func(node, source string, addVolume float64) {
		if sources[node] == nil {
			sources[node] = make(map[string]bool)
		}
		sources[node][source] = true
		volume[node] += addVolume
	}

	for key, acc := range edges {
		src, dst := key[0], key[1]
		if acc.hasSpans {
			touch(src, sourceSpans, float64(acc.callCount))
			touch(dst, sourceSpans, float64(acc.callCount))
		}
		if acc.hasNetwork {
			touch(src, sourceNetwork, float64(acc.bytesTotal))
			touch(dst, sourceNetwork, float64(acc.bytesTotal))
		}
	}

	return sources, volume
}

// rankAndCollapse returns the nodes kept by name: past maxRankedNodes, the
// top maxRankedNodes-1 by volume, leaving one slot for "other".
func rankAndCollapse(volume map[string]float64) map[string]bool {
	if len(volume) <= maxRankedNodes {
		keep := make(map[string]bool, len(volume))
		for node := range volume {
			keep[node] = true
		}
		return keep
	}

	names := make([]string, 0, len(volume))
	for node := range volume {
		names = append(names, node)
	}
	sort.Slice(names, func(i, j int) bool {
		if volume[names[i]] != volume[names[j]] {
			return volume[names[i]] > volume[names[j]]
		}
		return names[i] < names[j] // stable tie-break, deterministic output
	})

	keep := make(map[string]bool, maxRankedNodes-1)
	for _, node := range names[:maxRankedNodes-1] {
		keep[node] = true
	}
	return keep
}

func rewrite(node string, keep map[string]bool) string {
	if keep[node] {
		return node
	}
	return otherNodeName
}

func collapseNodeSources(sources map[string]map[string]bool, keep map[string]bool) map[string]map[string]bool {
	final := make(map[string]map[string]bool)
	for node, nodeSources := range sources {
		target := rewrite(node, keep)
		if final[target] == nil {
			final[target] = make(map[string]bool)
		}
		for source := range nodeSources {
			final[target][source] = true
		}
	}
	return final
}

func collapseEdges(edges map[[2]string]*edgeAccumulator, keep map[string]bool) map[[2]string]*edgeAccumulator {
	final := make(map[[2]string]*edgeAccumulator)
	for key, acc := range edges {
		finalKey := [2]string{rewrite(key[0], keep), rewrite(key[1], keep)}
		target, ok := final[finalKey]
		if !ok {
			target = &edgeAccumulator{sources: make(map[string]bool)}
			final[finalKey] = target
		}
		for source := range acc.sources {
			target.sources[source] = true
		}
		if acc.hasSpans {
			target.hasSpans = true
			target.callCount += acc.callCount
			target.errorCount += acc.errorCount
			target.durationMsWeighted += acc.durationMsWeighted
		}
		if acc.hasNetwork {
			target.hasNetwork = true
			target.bytesTotal += acc.bytesTotal
		}
	}
	return final
}

func buildNodeDTOs(
	sources map[string]map[string]bool,
	k8sMetadata map[string]k8sInfo,
) []ServiceMapNodeDTO {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)

	nodes := make([]ServiceMapNodeDTO, 0, len(names))
	for _, name := range names {
		dto := ServiceMapNodeDTO{Name: name, Sources: sortedKeys(sources[name])}
		// "other" is a bucket, not one service, so it gets no K8s metadata.
		if name != otherNodeName {
			if info, ok := k8sMetadata[name]; ok {
				dto.Namespace = info.Namespace
				dto.Deployment = info.Deployment
				dto.Node = info.Node
			}
		}
		nodes = append(nodes, dto)
	}
	return nodes
}

func buildEdgeDTOs(edges map[[2]string]*edgeAccumulator) []ServiceMapEdgeDTO {
	keys := make([][2]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})

	dtoEdges := make([]ServiceMapEdgeDTO, 0, len(keys))
	for _, key := range keys {
		acc := edges[key]
		dto := ServiceMapEdgeDTO{Src: key[0], Dst: key[1], Sources: sortedKeys(acc.sources)}
		if acc.hasSpans {
			callCount := acc.callCount
			dto.CallCount = &callCount
			if callCount > 0 {
				errorRate := float64(acc.errorCount) / float64(callCount)
				dto.ErrorRate = &errorRate
				avgLatencyMs := acc.durationMsWeighted / float64(callCount)
				dto.AvgLatencyMs = &avgLatencyMs
			}
		}
		if acc.hasNetwork {
			bytesTotal := acc.bytesTotal
			dto.BytesTotal = &bytesTotal
		}
		dtoEdges = append(dtoEdges, dto)
	}
	return dtoEdges
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
