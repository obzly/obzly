package main

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// The k8s metric profile: what an OTel Collector sends for the k8s logs
// profile's cluster, shaped like a production Site's (OTel host metrics per
// node, kubelet stats per pod and container, cluster state, and the apps'
// request-duration histograms): about 29 series a pod and 137 a node, so
// -pods 300 is about 10,500 series. Every series has a point each
// scrapeInterval. The simulated clock counts data points: point p is series
// p mod S of scrape p / S. Counters are cumulative from their pod's start, so
// they reset when a pod is replaced, which also replaces its series.

type metricKind int

const (
	metricGauge metricKind = iota
	metricCounter
	metricUpDown
	metricHistogram
)

// metricSeries is one series at one scrape.
type metricSeries struct {
	resource string
	name     string
	unit     string
	kind     metricKind
	attrs    []*commonv1.KeyValue
	// seed sets the series' level and rate; start is when its counter began.
	seed  uint64
	start time.Time
}

type metricResource struct {
	attrs []*commonv1.KeyValue
}

const k8sMetricNodes = 12

var (
	cpuStates        = []string{"user", "system", "idle", "iowait", "nice", "steal", "softirq", "interrupt"}
	memoryStates     = []string{"used", "free", "buffered", "cached", "slab_reclaimable", "slab_unreclaimable"}
	mountpoints      = []string{"/", "/var/lib/kubelet", "/var/lib/containerd", "/boot"}
	filesystemStates = []string{"used", "free", "reserved"}
	disks            = []string{"nvme0n1", "nvme1n1", "nvme2n1"}
	interfaces       = []string{"eth0", "eth1", "cni0", "lo"}
	durationBounds   = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	// durationWeights: each bucket's share of requests (the last is +Inf).
	durationWeights = []float64{4, 10, 22, 24, 16, 10, 6, 4, 2, 1, 0.6, 0.4}
)

func newK8sMetricsSender(otlp *otlpSender, start time.Time, interval time.Duration, pods int) *k8sLogsSender {
	services := k8sServices(pods)
	perScrape := len(k8sMetricSeries(services, start))
	clock := &simClock{start: start, perLine: float64(interval) / float64(perScrape)}
	return &k8sLogsSender{
		otlp:    otlp,
		clock:   clock,
		profile: "k8s",
		build: func(rng *rand.Rand, first int64, n int) proto.Message {
			return buildK8sMetricsRequest(services, start, interval, perScrape, first, n)
		},
		backoff: 250 * time.Millisecond,
	}
}

// buildK8sMetricsRequest is data points first to first+n-1 of the scrape
// sequence, grouped by resource and metric as a collector batches them.
func buildK8sMetricsRequest(
	services []*k8sService, start time.Time, interval time.Duration, perScrape int, first int64, n int,
) *collectormetricsv1.ExportMetricsServiceRequest {
	type metricKey struct{ resource, name string }
	scrapes := map[int64][]metricSeries{}
	resources := map[string]*metricResource{}
	byMetric := map[metricKey]*metricsv1.Metric{}
	var resourceOrder []string
	metricsOf := map[string][]*metricsv1.Metric{}
	for p := first; p < first+int64(n); p++ {
		scrape := p / int64(perScrape)
		at := start.Add(time.Duration(scrape) * interval)
		series, ok := scrapes[scrape]
		if !ok {
			series = k8sMetricSeries(services, at)
			scrapes[scrape] = series
			for name, resource := range k8sMetricResources(services, at) {
				resources[name] = resource
			}
		}
		s := series[int(p%int64(perScrape))%len(series)]
		key := metricKey{s.resource, s.name}
		metric, ok := byMetric[key]
		if !ok {
			metric = newMetric(s)
			byMetric[key] = metric
			if len(metricsOf[s.resource]) == 0 {
				resourceOrder = append(resourceOrder, s.resource)
			}
			metricsOf[s.resource] = append(metricsOf[s.resource], metric)
		}
		addPoint(metric, s, at, scrape)
	}
	request := &collectormetricsv1.ExportMetricsServiceRequest{}
	for _, name := range resourceOrder {
		request.ResourceMetrics = append(request.ResourceMetrics, &metricsv1.ResourceMetrics{
			Resource:     &resourcev1.Resource{Attributes: resources[name].attrs},
			ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: metricsOf[name]}},
		})
	}
	return request
}

// k8sMetricSeries is every series of a scrape at at, in a fixed order whose
// length never changes (pods are replaced, not added).
func k8sMetricSeries(services []*k8sService, at time.Time) []metricSeries {
	var series []metricSeries
	add := func(resource, name, unit string, kind metricKind, start time.Time, attrs ...*commonv1.KeyValue) {
		key := resource + "/" + name
		for _, attr := range attrs {
			key += "/" + attr.Key + "=" + attr.Value.GetStringValue()
		}
		series = append(series, metricSeries{
			resource: resource, name: name, unit: unit, kind: kind, attrs: attrs, seed: stableUint(key), start: start,
		})
	}
	boot := at.Truncate(30 * 24 * time.Hour)
	for n := 1; n <= k8sMetricNodes; n++ {
		node := fmt.Sprintf("contoso-node-%02d", n)
		for cpu := range 4 {
			for _, state := range cpuStates {
				add(node, "system.cpu.time", "s", metricCounter, boot,
					strAttr("cpu", fmt.Sprintf("cpu%d", cpu)), strAttr("state", state))
			}
		}
		for _, state := range memoryStates {
			add(node, "system.memory.usage", "By", metricUpDown, boot, strAttr("state", state))
		}
		for _, window := range []string{"1m", "5m", "15m"} {
			add(node, "system.cpu.load_average."+window, "{thread}", metricGauge, boot)
		}
		for _, mount := range mountpoints {
			for _, state := range filesystemStates {
				add(node, "system.filesystem.usage", "By", metricUpDown, boot,
					strAttr("mountpoint", mount), strAttr("state", state))
				add(node, "system.filesystem.inodes.usage", "{inode}", metricUpDown, boot,
					strAttr("mountpoint", mount), strAttr("state", state))
			}
		}
		for _, disk := range disks {
			for _, direction := range []string{"read", "write"} {
				for _, name := range []string{"system.disk.io", "system.disk.operations", "system.disk.operation_time", "system.disk.merged"} {
					add(node, name, "", metricCounter, boot, strAttr("device", disk), strAttr("direction", direction))
				}
			}
			for _, name := range []string{"system.disk.io_time", "system.disk.weighted_io_time"} {
				add(node, name, "s", metricCounter, boot, strAttr("device", disk))
			}
			add(node, "system.disk.pending_operations", "{operation}", metricUpDown, boot, strAttr("device", disk))
		}
		for _, iface := range interfaces {
			for _, direction := range []string{"transmit", "receive"} {
				for _, name := range []string{"system.network.io", "system.network.packets", "system.network.errors", "system.network.dropped"} {
					add(node, name, "", metricCounter, boot, strAttr("device", iface), strAttr("direction", direction))
				}
			}
		}
		for _, direction := range []string{"page_in", "page_out"} {
			for _, kind := range []string{"major", "minor"} {
				add(node, "system.paging.operations", "{operation}", metricCounter, boot,
					strAttr("direction", direction), strAttr("type", kind))
			}
		}
		for _, status := range []string{"running", "blocked", "sleeping"} {
			add(node, "system.processes.count", "{process}", metricUpDown, boot, strAttr("status", status))
		}
		add("cluster", "k8s.node.condition_ready", "", metricGauge, boot, strAttr("k8s.node.name", node))
		add("cluster", "k8s.node.allocatable_cpu", "{cpu}", metricGauge, boot, strAttr("k8s.node.name", node))
		add("cluster", "k8s.node.allocatable_memory", "By", metricGauge, boot, strAttr("k8s.node.name", node))
	}
	for _, svc := range services {
		add("cluster", "k8s.deployment.desired", "{pod}", metricGauge, boot,
			strAttr("k8s.deployment.name", svc.name), strAttr("k8s.namespace.name", svc.namespace))
		add("cluster", "k8s.deployment.available", "{pod}", metricGauge, boot,
			strAttr("k8s.deployment.name", svc.name), strAttr("k8s.namespace.name", svc.namespace))
		routes := serviceRoutes(svc)
		for replica := range svc.replicas {
			pod := svc.podAt(replica, at)
			started := time.Unix(0, at.UnixNano()/int64(svc.churn)*int64(svc.churn))
			for _, name := range []string{"k8s.pod.cpu.time", "k8s.pod.memory.page_faults", "container.cpu.time"} {
				add(pod.name, name, "", metricCounter, started)
			}
			for _, name := range []string{
				"k8s.pod.cpu.usage", "k8s.pod.memory.usage", "k8s.pod.memory.working_set", "k8s.pod.memory.rss",
				"k8s.pod.filesystem.usage", "k8s.pod.filesystem.available", "k8s.pod.filesystem.capacity",
				"container.memory.usage", "k8s.container.cpu_limit", "k8s.container.memory_limit",
				"http.server.active_requests",
			} {
				add(pod.name, name, "", metricGauge, started)
			}
			add(pod.name, "k8s.container.restarts", "{restart}", metricGauge, started)
			for _, direction := range []string{"transmit", "receive"} {
				add(pod.name, "k8s.pod.network.io", "By", metricCounter, started, strAttr("direction", direction))
				add(
					pod.name,
					"k8s.pod.network.errors",
					"{error}",
					metricCounter,
					started,
					strAttr("direction", direction),
				)
			}
			for _, volume := range []string{"data", "kube-api-access"} {
				add(pod.name, "k8s.volume.available", "By", metricGauge, started, strAttr("k8s.volume.name", volume))
				add(pod.name, "k8s.volume.capacity", "By", metricGauge, started, strAttr("k8s.volume.name", volume))
			}
			for _, route := range routes {
				for _, status := range []string{"200", "500"} {
					add(pod.name, "http.server.request.duration", "s", metricHistogram, started,
						strAttr("http.route", route), strAttr("http.response.status_code", status))
				}
			}
		}
	}
	return series
}

// k8sMetricResources is each resource a scrape at at reports under.
func k8sMetricResources(services []*k8sService, at time.Time) map[string]*metricResource {
	resources := map[string]*metricResource{
		"cluster": {
			attrs: []*commonv1.KeyValue{strAttr("k8s.cluster.name", "contoso-lab"), strAttr("loadtest", "k8s")},
		},
	}
	for n := 1; n <= k8sMetricNodes; n++ {
		node := fmt.Sprintf("contoso-node-%02d", n)
		resources[node] = &metricResource{attrs: []*commonv1.KeyValue{
			strAttr("host.name", node), strAttr("k8s.node.name", node), strAttr("os.type", "linux"),
			strAttr("k8s.cluster.name", "contoso-lab"), strAttr("loadtest", "k8s"),
		}}
	}
	for _, svc := range services {
		for replica := range svc.replicas {
			pod := svc.podAt(replica, at)
			resources[pod.name] = &metricResource{attrs: k8sResource(pod).Attributes}
		}
	}
	return resources
}

// serviceRoutes: the routes a service's pods report request durations for
// (three, none for workers).
func serviceRoutes(svc *k8sService) []string {
	if len(svc.kinds) > 0 && svc.kinds[0].kind == kindKafka {
		return nil
	}
	routes := make([]string, 3)
	for i := range routes {
		routes[i] = k8sRoutes[(stableUint(svc.name)+uint64(i))%uint64(len(k8sRoutes))]
	}
	return routes
}

func newMetric(s metricSeries) *metricsv1.Metric {
	metric := &metricsv1.Metric{Name: s.name, Unit: s.unit}
	switch s.kind {
	case metricGauge:
		metric.Data = &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{}}
	case metricCounter, metricUpDown:
		metric.Data = &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
			IsMonotonic:            s.kind == metricCounter,
			AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
		}}
	case metricHistogram:
		metric.Data = &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{
			AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
		}}
	}
	return metric
}

// addPoint adds s's value at at (scrape number scrape) to metric.
func addPoint(metric *metricsv1.Metric, s metricSeries, at time.Time, scrape int64) {
	level := 1 + float64(s.seed%1000)
	elapsed := math.Max(at.Sub(s.start).Seconds(), 0)
	switch s.kind {
	case metricHistogram:
		rate := 0.2 + float64(s.seed%50)/10
		if s.attrs[1].Value.GetStringValue() == "500" {
			rate /= 40
		}
		count := uint64(rate * elapsed)
		buckets := make([]uint64, len(durationWeights))
		total := 0.0
		for _, weight := range durationWeights {
			total += weight
		}
		var assigned uint64
		cumulative := 0.0
		for i, weight := range durationWeights {
			cumulative += weight
			upTo := uint64(float64(count) * cumulative / total)
			buckets[i] = upTo - assigned
			assigned = upTo
		}
		histogram := metric.GetHistogram()
		histogram.DataPoints = append(histogram.DataPoints, &metricsv1.HistogramDataPoint{
			StartTimeUnixNano: uint64(s.start.UnixNano()),
			TimeUnixNano:      uint64(at.UnixNano()),
			Attributes:        s.attrs,
			Count:             count,
			Sum:               proto.Float64(float64(count) * 0.12),
			BucketCounts:      buckets,
			ExplicitBounds:    durationBounds,
		})
	case metricCounter:
		sum := metric.GetSum()
		sum.DataPoints = append(sum.DataPoints, &metricsv1.NumberDataPoint{
			StartTimeUnixNano: uint64(s.start.UnixNano()),
			TimeUnixNano:      uint64(at.UnixNano()),
			Attributes:        s.attrs,
			Value:             &metricsv1.NumberDataPoint_AsDouble{AsDouble: level / 100 * elapsed},
		})
	default:
		// A level that wanders a little from scrape to scrape.
		wobble := float64(stableUint(fmt.Sprintf("%d/%d", s.seed, scrape))%200)/1000 - 0.1
		point := &metricsv1.NumberDataPoint{
			StartTimeUnixNano: uint64(s.start.UnixNano()),
			TimeUnixNano:      uint64(at.UnixNano()),
			Attributes:        s.attrs,
			Value:             &metricsv1.NumberDataPoint_AsDouble{AsDouble: level * (1 + wobble)},
		}
		if s.kind == metricGauge {
			metric.GetGauge().DataPoints = append(metric.GetGauge().DataPoints, point)
		} else {
			metric.GetSum().DataPoints = append(metric.GetSum().DataPoints, point)
		}
	}
}
