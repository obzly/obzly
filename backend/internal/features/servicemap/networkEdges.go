package servicemap

import (
	"context"
	"fmt"
	"math"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
)

// networkMetricsQueryTimeout: there is no request context to inherit a deadline from.
const networkMetricsQueryTimeout = 30 * time.Second

// OBI's network metrics and labels as stored by the OTLP ingest path. The
// packets metric name follows OBI's docs and is unverified; an absent series
// just yields zero packets.
const (
	obiNetworkBytesMetric   = "obi_network_flow_bytes"
	obiNetworkPacketsMetric = "obi_network_flow_packets"

	labelSrcOwnerName = "k8s_src_owner_name"
	labelDstOwnerName = "k8s_dst_owner_name"
	labelSrcNamespace = "k8s_src_namespace"
	labelDstNamespace = "k8s_dst_namespace"
	labelSrcOwnerType = "k8s_src_owner_type"
	labelDstOwnerType = "k8s_dst_owner_type"
	labelHostID       = "host_id"

	ownerTypeService = "Service"

	// networkBaselinePad widens the read backwards so a short window (Live
	// mode: 60s) still holds two samples of a once-a-minute counter;
	// without it edges flickered between polls.
	networkBaselinePad = 3 * time.Minute
)

// NetworkEdge is one src->dst service pair from OBI's network metrics, the
// network counterpart of spans_core.ServiceCallEdge.
type NetworkEdge struct {
	SrcService   string
	DstService   string
	BytesTotal   int64
	PacketsTotal int64
}

// networkK8sInfo has only a namespace: OBI's network metrics carry no
// pod/node label.
type networkK8sInfo struct {
	Namespace string
}

// networkEdges sums OBI's network series per (src, dst) k8s owner name. The
// counter increase is approximate (see counterIncrease): fine for an edge
// weight, not a billing number.
func networkEdges(
	projectID uuid.UUID,
	from, to time.Time,
) ([]NetworkEdge, map[string]networkK8sInfo, error) {
	windowSeconds := to.Sub(from).Seconds()
	readFrom := from.Add(-networkBaselinePad)

	bytesSeries, err := selectSeries(projectID, obiNetworkBytesMetric, readFrom, to)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to select %s: %w", obiNetworkBytesMetric, err)
	}
	packetsSeries, err := selectSeries(projectID, obiNetworkPacketsMetric, readFrom, to)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to select %s: %w", obiNetworkPacketsMetric, err)
	}

	type edgeKey struct{ src, dst string }
	bytesByEdge := make(map[edgeKey]int64)
	packetsByEdge := make(map[edgeKey]int64)
	k8sMetadata := make(map[string]networkK8sInfo)

	accumulate := func(series []metrics_core.SeriesDTO, into map[edgeKey]int64) {
		for _, s := range dropServiceAliases(series) {
			src := s.Labels[labelSrcOwnerName]
			dst := s.Labels[labelDstOwnerName]
			if src == "" || dst == "" {
				continue
			}
			into[edgeKey{src, dst}] += counterIncrease(s.Samples, windowSeconds)

			if ns := s.Labels[labelSrcNamespace]; ns != "" {
				k8sMetadata[src] = networkK8sInfo{Namespace: ns}
			}
			if ns := s.Labels[labelDstNamespace]; ns != "" {
				k8sMetadata[dst] = networkK8sInfo{Namespace: ns}
			}
		}
	}
	accumulate(bytesSeries, bytesByEdge)
	accumulate(packetsSeries, packetsByEdge)

	edgeKeys := make(map[edgeKey]bool, len(bytesByEdge)+len(packetsByEdge))
	for k := range bytesByEdge {
		edgeKeys[k] = true
	}
	for k := range packetsByEdge {
		edgeKeys[k] = true
	}

	edges := make([]NetworkEdge, 0, len(edgeKeys))
	for k := range edgeKeys {
		edges = append(edges, NetworkEdge{
			SrcService:   k.src,
			DstService:   k.dst,
			BytesTotal:   bytesByEdge[k],
			PacketsTotal: packetsByEdge[k],
		})
	}

	return edges, k8sMetadata, nil
}

// dropServiceAliases removes double-counted flows: on one node OBI reports a
// flow both against the workload and against the Service fronting it (e.g.
// coredns and kube-dns). A Service-typed side is dropped only when the same
// node reported a workload for the same peer, so a Service-only view (e.g.
// the "kubernetes" API Service) is kept. Trade-off: an unrelated
// Service-only series to that peer on that node is dropped too.
func dropServiceAliases(series []metrics_core.SeriesDTO) []metrics_core.SeriesDTO {
	type sideKey struct{ host, name, ownerType string }

	isWorkload := func(name, ownerType string) bool {
		return name != "" && ownerType != ownerTypeService
	}

	// peers with a workload on the opposite side, keyed by the side we hold.
	srcHasWorkloadPeer := make(map[sideKey]bool)
	dstHasWorkloadPeer := make(map[sideKey]bool)
	for _, s := range series {
		host := s.Labels[labelHostID]
		src, srcType := s.Labels[labelSrcOwnerName], s.Labels[labelSrcOwnerType]
		dst, dstType := s.Labels[labelDstOwnerName], s.Labels[labelDstOwnerType]
		if isWorkload(dst, dstType) {
			srcHasWorkloadPeer[sideKey{host, src, srcType}] = true
		}
		if isWorkload(src, srcType) {
			dstHasWorkloadPeer[sideKey{host, dst, dstType}] = true
		}
	}

	kept := make([]metrics_core.SeriesDTO, 0, len(series))
	for _, s := range series {
		host := s.Labels[labelHostID]
		src, srcType := s.Labels[labelSrcOwnerName], s.Labels[labelSrcOwnerType]
		dst, dstType := s.Labels[labelDstOwnerName], s.Labels[labelDstOwnerType]

		// A Service source is an alias if a workload on this node already
		// sends to the same destination; likewise for a Service destination.
		if srcType == ownerTypeService && dstHasWorkloadPeer[sideKey{host, dst, dstType}] {
			continue
		}
		if dstType == ownerTypeService && srcHasWorkloadPeer[sideKey{host, src, srcType}] {
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

func selectSeries(
	projectID uuid.UUID,
	metricName string,
	from, to time.Time,
) ([]metrics_core.SeriesDTO, error) {
	ctx, cancel := context.WithTimeout(context.Background(), networkMetricsQueryTimeout)
	defer cancel()
	response, err := metrics_core.GetMetricCoreRepository().
		SelectSeries(ctx, projectID, &metrics_core.SelectSeriesRequestDTO{
			Matchers:  []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: metricName}},
			TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
		})
	if err != nil {
		return nil, err
	}
	return response.Series, nil
}

// counterIncrease applies the first-to-last average rate to the window,
// never extrapolating past the observed span. Needs two samples (sorted
// ascending); a counter reset yields 0, not a negative.
func counterIncrease(samples []metrics_core.SampleDTO, windowSeconds float64) int64 {
	if len(samples) < 2 {
		return 0
	}
	first, last := samples[0], samples[len(samples)-1]
	delta := last.Value - first.Value
	spanSeconds := last.Timestamp.Sub(first.Timestamp).Seconds()
	if delta <= 0 || spanSeconds <= 0 {
		return 0
	}
	return int64(delta / spanSeconds * math.Min(windowSeconds, spanSeconds))
}
