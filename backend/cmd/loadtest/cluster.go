package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
)

// The cluster profile sends a whole cluster's logs the way an OTel Collector
// collects them: the k8s profile's applications, now written in Java, Go,
// Python and Node, as Deployments, StatefulSets and DaemonSets, with an
// istio-proxy sidecar beside the meshed ones; the cluster's own components
// (ingress-nginx, CoreDNS, kube-proxy, the autoscaler, cert-manager,
// fluent-bit, an OTel Collector); its data services (Postgres, Redis,
// Kafka); and a few plain VMs logging syslog. Each component keeps its real
// format, so pattern grouping sees the mix a real Site does. Every name,
// address and value is made up (Contoso, example.com, documentation and
// cluster-internal IP ranges).

type clusterKind int

const (
	clusterDeployment clusterKind = iota
	clusterStatefulSet
	clusterDaemonSet
	clusterHost
)

// clusterNodes is the cluster's node count: a DaemonSet has one pod each.
const clusterNodes = 12

type lineFunc func(rng *rand.Rand, pod clusterPod, ts time.Time) (level, body string)

type weightedLine struct {
	line   lineFunc
	weight int
}

type clusterContainer struct {
	name   string
	weight int
	lines  []weightedLine
}

// clusterWorkload is one Deployment, StatefulSet, DaemonSet or group of VMs;
// weight is its share of all lines.
type clusterWorkload struct {
	name       string
	namespace  string
	kind       clusterKind
	replicas   int
	churn      time.Duration
	weight     int
	containers []clusterContainer
}

type clusterPod struct {
	workload *clusterWorkload
	name     string
	node     string
}

var (
	clusterWords = []string{
		"parcel", "arrived", "damaged", "refund", "colour", "wrong", "size", "gift", "wrapping", "late",
		"courier", "address", "changed", "voucher", "expired", "loyalty", "points", "missing", "invoice", "twice",
		"charged", "please", "cancel", "swap", "warranty", "broken", "zip", "stitching", "smaller", "than",
		"expected", "lovely", "quality", "return", "label", "printer", "office", "weekend", "delivery", "neighbour",
	}
	pythonModules  = []string{"recs.scoring", "recs.features", "etl.extract", "etl.load", "support.tickets"}
	goCallers      = []string{"session/refresh.go", "session/store.go", "flags/evaluate.go", "http/middleware.go"}
	nodeRoutes     = []string{"/", "/checkout", "/checkout/shipping", "/checkout/payment", "/checkout/confirm"}
	clusterDomains = []string{
		"orders-api.shop.svc.cluster.local", "carts-api.shop.svc.cluster.local", "postgres.data.svc.cluster.local",
		"redis.data.svc.cluster.local", "kafka.data.svc.cluster.local", "identity-api.identity.svc.cluster.local",
		"hooks.example.com", "api.example.net",
	}
)

func newClusterLogsSender(otlp *otlpSender, start time.Time, linesPerSecond float64, pods int) *k8sLogsSender {
	clock := &simClock{start: start, perLine: float64(time.Second) / linesPerSecond}
	workloads := clusterWorkloads(pods)
	return &k8sLogsSender{
		otlp:    otlp,
		clock:   clock,
		profile: "cluster",
		build: func(rng *rand.Rand, first int64, n int) *collectorlogsv1.ExportLogsServiceRequest {
			return buildClusterLogsRequest(rng, workloads, clock, first, n, 6)
		},
		backoff: 250 * time.Millisecond,
	}
}

// clusterWorkloads lays the cluster out; the Deployments share about `pods`
// replicas, StatefulSets and VMs have fixed counts and DaemonSets one pod per
// node.
func clusterWorkloads(pods int) []*clusterWorkload {
	app := func(name string, kinds []weightedKind) clusterContainer {
		return clusterContainer{name: name, weight: 100, lines: fromK8sKinds(kinds)}
	}
	mesh := func(name string, kinds []weightedKind) []clusterContainer {
		return []clusterContainer{
			app(name, kinds),
			{name: "istio-proxy", weight: 20, lines: []weightedLine{{envoyAccessLine, 1}}},
		}
	}
	one := func(name string, lines ...weightedLine) []clusterContainer {
		return []clusterContainer{{name: name, weight: 100, lines: lines}}
	}
	probe := weightedLine{fromK8sKind(kindProbe), 15}
	type spec struct {
		name, namespace string
		kind            clusterKind
		replicas, share int // share: Deployments' part of the replicas
		weight          int
		containers      []clusterContainer
	}
	specs := []spec{
		{"ingress-nginx-controller", "ingress-nginx", clusterDeployment, 0, 3, 50,
			one("controller", weightedLine{nginxAccessLine, 92}, weightedLine{nginxControllerLine, 8})},
		{"storefront-web", "edge", clusterDeployment, 0, 8, 55, []clusterContainer{app("storefront-web", k8sWebKinds)}},
		{"checkout-web", "edge", clusterDeployment, 0, 4, 30,
			one("checkout-web", weightedLine{pinoLine, 80}, probe)},
		{"orders-api", "shop", clusterDeployment, 0, 6, 40, mesh("orders-api", k8sAPIKinds)},
		{"carts-api", "shop", clusterDeployment, 0, 5, 35, mesh("carts-api", k8sAPIKinds)},
		{"catalog-api", "shop", clusterDeployment, 0, 6, 40, mesh("catalog-api", k8sAPIKinds)},
		{"pricing-api", "shop", clusterDeployment, 0, 3, 20, mesh("pricing-api", k8sAPIKinds)},
		{"inventory-api", "shop", clusterDeployment, 0, 4, 25, mesh("inventory-api", k8sAPIKinds)},
		{"payments-api", "payments", clusterDeployment, 0, 4, 25, mesh("payments-api", k8sAPIKinds)},
		{"card-gateway", "payments", clusterDeployment, 0, 3, 18, mesh("card-gateway", k8sAPIKinds)},
		{
			"refunds-worker",
			"payments",
			clusterDeployment,
			0,
			2,
			8,
			[]clusterContainer{app("refunds-worker", k8sWorkerKinds)},
		},
		{"tax-service", "payments", clusterDeployment, 0, 2, 10, mesh("tax-service", k8sAPIKinds)},
		{"identity-api", "identity", clusterDeployment, 0, 3, 18, mesh("identity-api", k8sAPIKinds)},
		{"sessions-api", "identity", clusterDeployment, 0, 3, 22,
			one("sessions-api", weightedLine{zapLine, 85}, weightedLine{goPanicLine, 1}, probe)},
		{"search-api", "search", clusterDeployment, 0, 5, 30, []clusterContainer{app("search-api", k8sAPIKinds)}},
		{
			"search-indexer",
			"search",
			clusterDeployment,
			0,
			3,
			15,
			[]clusterContainer{app("search-indexer", k8sWorkerKinds)},
		},
		{"recommendations", "search", clusterDeployment, 0, 3, 18,
			one("recommendations", weightedLine{pythonLine, 90}, weightedLine{pythonTracebackLine, 2}, probe)},
		{
			"order-projector",
			"data",
			clusterDeployment,
			0,
			3,
			15,
			[]clusterContainer{app("order-projector", k8sWorkerKinds)},
		},
		{
			"shipping-worker",
			"data",
			clusterDeployment,
			0,
			3,
			12,
			[]clusterContainer{app("shipping-worker", k8sWorkerKinds)},
		},
		{"reporting-etl", "data", clusterDeployment, 0, 2, 8,
			one("reporting-etl", weightedLine{pythonLine, 90}, weightedLine{pythonTracebackLine, 3})},
		{
			"email-sender",
			"platform",
			clusterDeployment,
			0,
			2,
			8,
			[]clusterContainer{app("email-sender", k8sWorkerKinds)},
		},
		{
			"audit-writer",
			"platform",
			clusterDeployment,
			0,
			2,
			8,
			[]clusterContainer{app("audit-writer", k8sWorkerKinds)},
		},
		{"feature-flags", "platform", clusterDeployment, 0, 1, 6,
			one("feature-flags", weightedLine{zapLine, 90}, probe)},
		{"support-desk", "platform", clusterDeployment, 0, 1, 4,
			one("support-desk", weightedLine{pythonLine, 80}, weightedLine{supportNoteLine, 20})},
		{"coredns", "kube-system", clusterDeployment, 0, 2, 50, one("coredns", weightedLine{coreDNSLine, 1})},
		{
			"cluster-autoscaler",
			"kube-system",
			clusterDeployment,
			0,
			1,
			3,
			one("cluster-autoscaler", weightedLine{autoscalerLine, 1}),
		},
		{
			"cert-manager",
			"cert-manager",
			clusterDeployment,
			0,
			1,
			2,
			one("cert-manager", weightedLine{certManagerLine, 1}),
		},
		{
			"otel-collector",
			"monitoring",
			clusterDeployment,
			0,
			2,
			10,
			one("otel-collector", weightedLine{otelCollectorLine, 1}),
		},
		{
			"kube-proxy",
			"kube-system",
			clusterDaemonSet,
			clusterNodes,
			0,
			10,
			one("kube-proxy", weightedLine{kubeProxyLine, 1}),
		},
		{
			"fluent-bit",
			"monitoring",
			clusterDaemonSet,
			clusterNodes,
			0,
			8,
			one("fluent-bit", weightedLine{fluentBitLine, 1}),
		},
		{
			"postgres",
			"data",
			clusterStatefulSet,
			2,
			0,
			20,
			one("postgres", weightedLine{postgresLine, 85}, weightedLine{fromK8sKind(kindSlowQuery), 15}),
		},
		{"redis", "data", clusterStatefulSet, 3, 0, 6, one("redis", weightedLine{redisLine, 1})},
		{"kafka", "data", clusterStatefulSet, 3, 0, 14, one("kafka", weightedLine{kafkaBrokerLine, 1})},
		{"contoso-bastion", "", clusterHost, 1, 0, 2, one("syslog", weightedLine{syslogLine, 1})},
		{"contoso-legacy-erp", "", clusterHost, 2, 0, 10,
			one("erp", weightedLine{syslogLine, 30}, weightedLine{fromK8sKind(kindKeyValue), 50},
				weightedLine{fromK8sKind(kindStackTrace), 2}, weightedLine{fromK8sKind(kindFreeText), 18})},
	}
	shares := 0
	for _, sp := range specs {
		shares += sp.share
	}
	workloads := make([]*clusterWorkload, len(specs))
	for i, sp := range specs {
		replicas := sp.replicas
		if sp.kind == clusterDeployment {
			replicas = max(1, int(math.Round(float64(pods*sp.share)/float64(shares))))
		}
		workloads[i] = &clusterWorkload{
			name: sp.name, namespace: sp.namespace, kind: sp.kind, replicas: replicas,
			churn: time.Duration(2+(i*7)%25) * time.Hour, weight: sp.weight, containers: sp.containers,
		}
	}
	return workloads
}

// podAt names a workload's replica at a moment, without keeping state:
// Deployment pods change with each churn period, StatefulSet pods keep
// their ordinal, a DaemonSet's pod belongs to its node and VMs are hosts.
func (w *clusterWorkload) podAt(replica int, at time.Time) clusterPod {
	epoch := at.UnixNano() / int64(w.churn)
	node := fmt.Sprintf(
		"contoso-node-%02d",
		1+int(stableUint(fmt.Sprintf("%s/%d/%d/node", w.name, replica, epoch))%clusterNodes),
	)
	switch w.kind {
	case clusterStatefulSet:
		return clusterPod{workload: w, name: fmt.Sprintf("%s-%d", w.name, replica), node: node}
	case clusterDaemonSet:
		week := at.UnixNano() / int64(7*24*time.Hour)
		return clusterPod{
			workload: w,
			name: fmt.Sprintf(
				"%s-%s",
				w.name,
				stableToken(fmt.Sprintf("%s/%d/%d", w.name, replica, week), podSuffixes, 5),
			),
			node: fmt.Sprintf("contoso-node-%02d", 1+replica%clusterNodes),
		}
	case clusterHost:
		return clusterPod{workload: w, name: fmt.Sprintf("%s-%02d", w.name, replica+1)}
	}
	rs := stableToken(fmt.Sprintf("%s/rs/%d", w.name, epoch/6), alnumLower, 10)
	suffix := stableToken(fmt.Sprintf("%s/%d/%d", w.name, replica, epoch), podSuffixes, 5)
	return clusterPod{workload: w, name: fmt.Sprintf("%s-%s-%s", w.name, rs, suffix), node: node}
}

func buildClusterLogsRequest(
	rng *rand.Rand,
	workloads []*clusterWorkload,
	clock *simClock,
	first int64,
	n, podsPer int,
) *collectorlogsv1.ExportLogsServiceRequest {
	at := clock.at(first)
	pods := make([]clusterPod, podsPer)
	for i := range pods {
		w := pickWorkload(rng, workloads)
		pods[i] = w.podAt(rng.Intn(w.replicas), at)
	}
	type stream struct{ pod, container int }
	records := map[stream][]*logsv1.LogRecord{}
	var order []stream
	for i := 0; i < n; i++ {
		p := rng.Intn(podsPer)
		c := pickContainer(rng, pods[p].workload.containers)
		ts := clock.at(first + int64(i))
		level, body := pickLine(rng, pods[p].workload.containers[c].lines)(rng, pods[p], ts)
		key := stream{p, c}
		if _, ok := records[key]; !ok {
			order = append(order, key)
		}
		records[key] = append(records[key], &logsv1.LogRecord{
			TimeUnixNano:   uint64(ts.UnixNano()),
			SeverityText:   level,
			SeverityNumber: otlpSeverities[level],
			Body:           &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: body}},
		})
	}
	request := &collectorlogsv1.ExportLogsServiceRequest{}
	for _, key := range order {
		pod := pods[key.pod]
		request.ResourceLogs = append(request.ResourceLogs, &logsv1.ResourceLogs{
			Resource:  clusterResource(pod, pod.workload.containers[key.container].name),
			ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: records[key]}},
		})
	}
	return request
}

func clusterResource(pod clusterPod, container string) *resourcev1.Resource {
	w := pod.workload
	if w.kind == clusterHost {
		return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
			strAttr("service.name", w.name),
			strAttr("host.name", pod.name),
			strAttr("os.type", "linux"),
			strAttr("loadtest", "cluster"),
		}}
	}
	workloadKey := map[clusterKind]string{
		clusterDeployment:  "k8s.deployment.name",
		clusterStatefulSet: "k8s.statefulset.name",
		clusterDaemonSet:   "k8s.daemonset.name",
	}[w.kind]
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		strAttr("service.name", w.name),
		strAttr("k8s.namespace.name", w.namespace),
		strAttr(workloadKey, w.name),
		strAttr("k8s.pod.name", pod.name),
		strAttr("k8s.container.name", container),
		strAttr("k8s.node.name", pod.node),
		strAttr("k8s.cluster.name", "contoso-lab"),
		strAttr("loadtest", "cluster"),
	}}
}

func fromK8sKind(kind k8sLineKind) lineFunc {
	return func(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
		return k8sLine(rng, kind, k8sPod{
			service: &k8sService{name: pod.workload.name, namespace: pod.workload.namespace},
			name:    pod.name, node: pod.node,
		}, ts)
	}
}

func fromK8sKinds(kinds []weightedKind) []weightedLine {
	lines := make([]weightedLine, len(kinds))
	for i, k := range kinds {
		lines[i] = weightedLine{fromK8sKind(k.kind), k.weight}
	}
	return lines
}

// envoyAccessLine is istio-proxy's default access log for a request it
// passes to the app beside it.
func envoyAccessLine(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
	route := pick(rng, k8sRoutes)
	status := pickWeighted(rng, []int{200, 200, 200, 200, 200, 201, 204, 404, 503})
	flags := "-"
	level := "INFO"
	if status == 503 {
		flags, level = "UF,URX", "WARN"
	}
	host := pod.workload.name + "." + pod.workload.namespace + ".svc.cluster.local"
	return level, fmt.Sprintf(
		`[%s] "%s %s HTTP/1.1" %d %s via_upstream - "-" %d %d %d %d "%s" "%s" "%s" "%s:8080" "%s:8080" inbound|8080|| 127.0.0.6:%d %s:8080 %s:%d outbound_.8080_._.%s default`,
		ts.UTC().Format("2006-01-02T15:04:05.000Z"),
		pick(rng, []string{"GET", "GET", "POST", "PUT"}),
		strings.Replace(route, "{id}", idToken(rng, route), 1),
		status,
		flags,
		rng.Intn(2000),
		100+rng.Intn(20_000),
		rng.Intn(300),
		rng.Intn(300),
		docIP(rng),
		pick(rng, k8sUserAgents),
		uuidToken(rng),
		host,
		podIP(rng),
		30_000+rng.Intn(30_000),
		podIP(rng),
		podIP(rng),
		30_000+rng.Intn(30_000),
		host,
	)
}

// nginxAccessLine is ingress-nginx's default log format.
func nginxAccessLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	status := pickWeighted(rng, []int{200, 200, 200, 200, 200, 200, 304, 301, 404, 404, 499, 502, 503})
	upstream := pick(
		rng,
		[]string{"edge-storefront-web-80", "edge-checkout-web-80", "shop-orders-api-8080", "search-search-api-8080"},
	)
	path := pick(rng, []string{
		"/",
		"/collections/winter",
		fmt.Sprintf("/products/sku-%05d", rng.Intn(40_000)),
		fmt.Sprintf(
			"/api/v1/orders/%s",
			orderID(rng),
		),
		fmt.Sprintf("/search?q=%s", strings.ToLower(pick(rng, k8sItems))),
		"/checkout",
		"/static/app." + hexToken(rng, 8) + ".js",
	})
	level := "INFO"
	if status >= 500 {
		level = "ERROR"
	}
	took := rng.Float64() * 0.8
	return level, fmt.Sprintf(`%s - - [%s] "%s %s HTTP/2.0" %d %d "%s" "%s" %d %.3f [%s] [] %s:8080 %d %.3f %d %s`,
		docIP(rng), ts.UTC().Format("02/Jan/2006:15:04:05 -0700"), pick(rng, []string{"GET", "GET", "GET", "POST"}),
		path, status, 100+rng.Intn(60_000), pick(rng, k8sReferrers), pick(rng, k8sUserAgents), 200+rng.Intn(900), took,
		upstream, podIP(rng), 100+rng.Intn(60_000), took*0.95, status, hexToken(rng, 32))
}

func nginxControllerLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	switch rng.Intn(4) {
	case 0:
		return "WARN", klog(
			'W',
			ts,
			7,
			"controller.go:1214",
			fmt.Sprintf(
				`Service "%s" does not have any active Endpoint.`,
				pick(rng, []string{"edge/admin-web", "shop/pricing-api"}),
			),
		)
	case 1:
		return "INFO", klog('I', ts, 7, "controller.go:193", "Configuration changes detected, backend reload required.")
	case 2:
		return "INFO", klog('I', ts, 7, "controller.go:213", "Backend successfully reloaded.")
	default:
		return "INFO", klog('I', ts, 7, "event.go:377", fmt.Sprintf(
			`Event(v1.ObjectReference{Kind:"Ingress", Namespace:"edge", Name:"%s", APIVersion:"networking.k8s.io/v1"}): type: 'Normal' reason: 'Sync' Scheduled for sync`,
			pick(rng, []string{"storefront", "checkout", "admin"}),
		))
	}
}

// coreDNSLine is CoreDNS's log plugin: most lookups of a short name go
// through the search path first, so NXDOMAIN answers outnumber the rest.
func coreDNSLine(rng *rand.Rand, _ clusterPod, _ time.Time) (string, string) {
	name := pick(rng, clusterDomains) + "."
	answer, flags := "NOERROR", "qr,aa,rd"
	if rng.Intn(3) > 0 {
		name += pick(rng, []string{"shop.svc.cluster.local.", "svc.cluster.local.", "cluster.local."})
		answer = "NXDOMAIN"
	}
	if strings.HasSuffix(strings.TrimSuffix(name, "."), "example.com") ||
		strings.HasSuffix(strings.TrimSuffix(name, "."), "example.net") {
		flags = "qr,rd,ra"
	}
	return "INFO", fmt.Sprintf(`[INFO] %s:%d - %d "%s IN %s udp %d false 512" %s %s %d %.6fs`,
		podIP(rng), 30_000+rng.Intn(30_000), rng.Intn(65_536), pick(rng, []string{"A", "A", "AAAA"}), name,
		40+rng.Intn(40), answer, flags, 100+rng.Intn(100), rng.Float64()*0.003)
}

func kubeProxyLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	if rng.Intn(10) == 0 {
		return "INFO", klog('I', ts, 1, "proxier.go:805", `"Syncing iptables rules" ipFamily="IPv4"`)
	}
	return "INFO", klog('I', ts, 1, "proxier.go:1494", fmt.Sprintf(
		`"Reloading service iptables data" numServices=%d numEndpoints=%d numFilterChains=6 numFilterRules=%d numNATChains=%d numNATRules=%d`,
		60+rng.Intn(5),
		140+rng.Intn(40),
		8+rng.Intn(6),
		120+rng.Intn(40),
		300+rng.Intn(80),
	))
}

func autoscalerLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	node := fmt.Sprintf("contoso-node-%02d", 1+rng.Intn(clusterNodes))
	switch rng.Intn(5) {
	case 0:
		return "INFO", klog(
			'I',
			ts,
			1,
			"scale_up.go:477",
			fmt.Sprintf("Best option to resize: contoso-pool-%c", 'a'+rune(rng.Intn(3))),
		)
	case 1:
		return "INFO", klog('I', ts, 1, "scale_down.go:886", fmt.Sprintf(`Scale-down: removing empty node "%s"`, node))
	case 2:
		return "INFO", klog('I', ts, 1, "cluster.go:149", fmt.Sprintf(
			"Node %s - cpu requested is %.2f%% of allocatable", node, 20+rng.Float64()*70))
	default:
		return "INFO", klog('I', ts, 1, "static_autoscaler.go:274", "Starting main loop")
	}
}

func certManagerLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	cert := pick(rng, []string{"edge/storefront-tls", "edge/checkout-tls", "identity/identity-tls"})
	if rng.Intn(3) == 0 {
		return "INFO", klog('I', ts, 1, "conditions.go:192", fmt.Sprintf(
			`"Found status change for Certificate condition; setting lastTransitionTime" certificate="%s" condition="Ready" oldStatus="False" status="True"`,
			cert,
		))
	}
	return "INFO", klog('I', ts, 1, "controller.go:162", fmt.Sprintf(
		`"re-queuing item due to optimistic locking on resource" logger="cert-manager.controller" key="%s"`, cert))
}

func fluentBitLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("2006/01/02 15:04:05")
	if rng.Intn(4) == 0 {
		return "WARN", fmt.Sprintf(
			"[%s] [ warn] [engine] failed to flush chunk '1-%d.%09d.flb', retry in %d seconds: task_id=%d, input=tail.0 > output=opentelemetry.0 (out_id=0)",
			at,
			ts.Unix(),
			rng.Intn(1_000_000_000),
			5+rng.Intn(20),
			rng.Intn(16),
		)
	}
	return "INFO", fmt.Sprintf(
		"[%s] [ info] [input:tail:tail.0] inotify_fs_add(): inode=%d watch_fd=%d name=/var/log/containers/%s-%s_%s.log",
		at,
		1_000_000+rng.Intn(9_000_000),
		rng.Intn(400),
		pick(rng, k8sUpstreams),
		token(rng, podSuffixes, 5),
		hexToken(rng, 64),
	)
}

func otelCollectorLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("2006-01-02T15:04:05.000Z")
	if rng.Intn(6) == 0 {
		return "WARN", fmt.Sprintf(
			"%s\twarn\tbatchprocessor@v0.115.0/batch_processor.go:263\tSender failed\t{\"kind\": \"processor\", \"name\": \"batch\", \"pipeline\": \"logs\", \"error\": \"rpc error: code = Unavailable desc = ingest buffer is full\"}",
			at,
		)
	}
	return "INFO", fmt.Sprintf(
		"%s\tinfo\tLogs\t{\"kind\": \"exporter\", \"data_type\": \"logs\", \"name\": \"otlphttp\", \"resource logs\": %d, \"log records\": %d}",
		at,
		1+rng.Intn(40),
		100+rng.Intn(5000),
	)
}

func postgresLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	prefix := fmt.Sprintf("%s UTC [%d]", ts.UTC().Format("2006-01-02 15:04:05.000"), 100+rng.Intn(60_000))
	table := pick(rng, k8sTables)
	switch rng.Intn(10) {
	case 0:
		return "INFO", fmt.Sprintf(
			"%s LOG:  checkpoint complete: wrote %d buffers (%.1f%%); 0 WAL file(s) added, 0 removed, %d recycled; write=%.3f s, sync=%.3f s, total=%.3f s",
			prefix,
			rng.Intn(20_000),
			rng.Float64()*30,
			rng.Intn(8),
			200+rng.Float64()*70,
			rng.Float64()*0.05,
			200+rng.Float64()*70,
		)
	case 1, 2:
		return "INFO", fmt.Sprintf(
			"%s LOG:  automatic vacuum of table \"shop.public.%s\": index scans: %d\n\tpages: 0 removed, %d remain, %d scanned (%.2f%% of total)\n\ttuples: %d removed, %d remain, 0 are dead but not yet removable",
			prefix,
			table,
			rng.Intn(2),
			10_000+rng.Intn(90_000),
			rng.Intn(5000),
			rng.Float64()*10,
			rng.Intn(50_000),
			100_000+rng.Intn(9_000_000),
		)
	case 3, 4, 5:
		return "INFO", fmt.Sprintf("%s LOG:  connection authorized: user=%s database=shop application_name=%s",
			prefix, strings.Split(pick(rng, k8sUpstreams), "-")[0], pick(rng, k8sUpstreams))
	case 6:
		return "ERROR", fmt.Sprintf(
			"%s ERROR:  deadlock detected\n%s DETAIL:  Process %d waits for ShareLock on transaction %d; blocked by process %d.\n\tProcess %d waits for ShareLock on transaction %d; blocked by process %d.\n%s HINT:  See server log for query details.",
			prefix,
			prefix,
			rng.Intn(60_000),
			rng.Intn(90_000_000),
			rng.Intn(60_000),
			rng.Intn(60_000),
			rng.Intn(90_000_000),
			rng.Intn(60_000),
			prefix,
		)
	default:
		return "INFO", fmt.Sprintf("%s LOG:  could not receive data from client: Connection reset by peer", prefix)
	}
}

func redisLine(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("02 Jan 2006 15:04:05.000")
	if strings.HasSuffix(pod.name, "-0") {
		child := 1000 + rng.Intn(60_000)
		switch rng.Intn(4) {
		case 0:
			return "INFO", fmt.Sprintf(
				"1:M %s * %d changes in %d seconds. Saving...",
				at,
				10+rng.Intn(10_000),
				pick2(rng, 60, 300),
			)
		case 1:
			return "INFO", fmt.Sprintf("1:M %s * Background saving started by pid %d", at, child)
		case 2:
			return "INFO", fmt.Sprintf("%d:C %s * Fork CoW for RDB: current %d MB, peak %d MB, average %d MB",
				child, at, rng.Intn(20), 1+rng.Intn(40), rng.Intn(20))
		default:
			return "INFO", fmt.Sprintf("1:M %s * Background saving terminated with success", at)
		}
	}
	if rng.Intn(2) == 0 {
		return "INFO", fmt.Sprintf("1:S %s * MASTER <-> REPLICA sync: Finished with success", at)
	}
	return "INFO", fmt.Sprintf("1:S %s * Connecting to MASTER redis-0.redis.data.svc.cluster.local:6379", at)
}

func kafkaBrokerLine(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("2006-01-02 15:04:05,000")
	partition := fmt.Sprintf("%s-%d", pick(rng, k8sTopics), rng.Intn(24))
	switch rng.Intn(5) {
	case 0:
		return "WARN", fmt.Sprintf(
			"[%s] WARN [ReplicaFetcher replicaId=%d, leaderId=%d, fetcherId=0] Error in response for fetch request (type=FetchRequest, replicaId=%d, maxWait=500, minBytes=1, maxBytes=10485760) (kafka.server.ReplicaFetcherThread)",
			at,
			len(pod.name)%3,
			rng.Intn(3),
			len(pod.name)%3,
		)
	case 1:
		return "INFO", fmt.Sprintf(
			"[%s] INFO [ProducerStateManager partition=%s] Wrote producer snapshot at offset %d with %d producer ids in %d ms. (kafka.log.ProducerStateManager)",
			at,
			partition,
			rng.Intn(90_000_000),
			rng.Intn(20),
			rng.Intn(10),
		)
	default:
		return "INFO", fmt.Sprintf(
			"[%s] INFO [LocalLog partition=%s, dir=/var/lib/kafka/data] Rolled new log segment at offset %d in %d ms. (kafka.log.LocalLog)",
			at,
			partition,
			rng.Intn(90_000_000),
			rng.Intn(12),
		)
	}
}

// zapLine is a Go service's zap JSON line.
func zapLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	stamp := float64(ts.UnixNano()) / 1e9
	switch rng.Intn(5) {
	case 0:
		return "WARN", fmt.Sprintf(
			`{"level":"warn","ts":%.6f,"caller":"%s:%d","msg":"session store slow","store":"redis","took":"%dms","key":"sess:%s"}`,
			stamp,
			pick(rng, goCallers),
			20+rng.Intn(300),
			50+rng.Intn(900),
			hexToken(rng, 24),
		)
	case 1:
		return "INFO", fmt.Sprintf(
			`{"level":"info","ts":%.6f,"caller":"flags/evaluate.go:%d","msg":"flag evaluated","flag":"%s","variant":"%s","tenant":"%s"}`,
			stamp,
			40+rng.Intn(80),
			pick(rng, []string{"new-checkout", "free-shipping-banner", "search-v2", "gift-cards"}),
			pick(rng, []string{"on", "off", "control", "treatment"}),
			pick(rng, []string{"contoso-nz", "fabrikam-us"}),
		)
	default:
		return "INFO", fmt.Sprintf(
			`{"level":"info","ts":%.6f,"caller":"session/refresh.go:%d","msg":"session refreshed","session_id":"%s","user_id":%s,"ttl":"30m0s"}`,
			stamp,
			80+rng.Intn(20),
			uuidToken(rng),
			userID(rng),
		)
	}
}

// goPanicLine is a Go panic: the runtime prints it to stderr in one burst.
func goPanicLine(rng *rand.Rand, _ clusterPod, _ time.Time) (string, string) {
	return "ERROR", fmt.Sprintf(
		"panic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x%x]\n\ngoroutine %d [running]:\ncontoso/sessions/session.(*Store).Refresh(0x0, {0x%x, 0x%x})\n\t/src/session/store.go:%d +0x%x\ncontoso/sessions/http.(*Handler).refresh(0x%x, {0x%x, 0x%x}, 0x%x)\n\t/src/http/handler.go:%d +0x%x\nnet/http.HandlerFunc.ServeHTTP(...)\n\t/usr/local/go/src/net/http/server.go:2220",
		0x400000+rng.Intn(
			0x100000,
		),
		1000+rng.Intn(90_000),
		rng.Intn(1<<30),
		rng.Intn(64),
		60+rng.Intn(40),
		rng.Intn(256),
		rng.Intn(1<<30),
		rng.Intn(1<<30),
		rng.Intn(64),
		rng.Intn(1<<30),
		100+rng.Intn(80),
		rng.Intn(256),
	)
}

// pythonLine is Python's logging module with a %(asctime)s %(levelname)s
// [%(name)s] format.
func pythonLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("2006-01-02 15:04:05,000")
	module := pick(rng, pythonModules)
	switch rng.Intn(4) {
	case 0:
		return "INFO", fmt.Sprintf("%s INFO [%s] Scored %d candidates for customer cust_%s in %dms",
			at, module, 10+rng.Intn(200), token(rng, alnumLower, 9), 5+rng.Intn(300))
	case 1:
		return "INFO", fmt.Sprintf("%s INFO [%s] Loaded %d rows into %s from s3://contoso-etl/%s/part-%05d.parquet",
			at, module, rng.Intn(500_000), pick(rng, k8sTables), ts.UTC().Format("2006/01/02"), rng.Intn(400))
	case 2:
		return "WARN", fmt.Sprintf("%s WARNING [%s] Feature store lookup for %s took %.2fs, using cached features",
			at, module, productName(rng), 0.5+rng.Float64()*3)
	default:
		return "INFO", fmt.Sprintf("%s INFO [%s] Ticket %d moved to %s by %s",
			at, module, 10_000+rng.Intn(90_000), pick(rng, []string{"open", "pending", "solved"}), email(rng))
	}
}

func pythonTracebackLine(rng *rand.Rand, _ clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("2006-01-02 15:04:05,000")
	site := rng.Intn(3)
	errors := []string{
		"KeyError: 'segment'",
		"psycopg2.errors.QueryCanceled: canceling statement due to statement timeout",
		"botocore.exceptions.ReadTimeoutError: Read timeout on endpoint URL: \"https://s3.example.com/contoso-etl\"",
	}
	return "ERROR", fmt.Sprintf(
		"%s ERROR [%s] Job failed\nTraceback (most recent call last):\n  File \"/app/%s/main.py\", line %d, in run\n    result = step(batch)\n  File \"/app/%s/steps.py\", line %d, in step\n    return transform(rows)\n%s",
		at,
		pick(rng, pythonModules),
		[]string{"recs", "etl", "etl"}[site],
		40+site*13,
		[]string{"recs", "etl", "etl"}[site],
		120+site*31,
		errors[site],
	)
}

// pinoLine is a Node service's pino JSON line (numeric levels).
func pinoLine(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
	status := pickWeighted(rng, []int{200, 200, 200, 200, 302, 404, 500})
	level, levelText := 30, "INFO"
	if status >= 500 {
		level, levelText = 50, "ERROR"
	}
	return levelText, fmt.Sprintf(
		`{"level":%d,"time":%d,"pid":1,"hostname":"%s","reqId":"req-%s","req":{"method":"GET","url":"%s","remoteAddress":"%s"},"res":{"statusCode":%d},"responseTime":%.1f,"msg":"request completed"}`,
		level,
		ts.UnixMilli(),
		pod.name,
		token(rng, alnumLower, 6),
		pick(rng, nodeRoutes),
		docIP(rng),
		status,
		rng.Float64()*250,
	)
}

// supportNoteLine carries free text customers wrote: almost every one is its
// own pattern, like the long tail on a real Site.
func supportNoteLine(rng *rand.Rand, _ clusterPod, _ time.Time) (string, string) {
	words := make([]string, 5+rng.Intn(8))
	for i := range words {
		words[i] = pick(rng, clusterWords)
	}
	return "INFO", fmt.Sprintf("Customer note on ticket %d: %s", 10_000+rng.Intn(90_000), strings.Join(words, " "))
}

func syslogLine(rng *rand.Rand, pod clusterPod, ts time.Time) (string, string) {
	at := ts.UTC().Format("Jan _2 15:04:05")
	pid := 300 + rng.Intn(60_000)
	switch rng.Intn(8) {
	case 0, 1:
		return "INFO", fmt.Sprintf(
			"%s %s sshd[%d]: Accepted publickey for %s from %s port %d ssh2: ED25519 SHA256:%s",
			at,
			pod.name,
			pid,
			pick(rng, []string{"deploy", "ops", "backup"}),
			docIP(rng),
			30_000+rng.Intn(30_000),
			token(rng, alnumUpper+alnumLower, 43),
		)
	case 2:
		return "WARN", fmt.Sprintf(
			"%s %s sshd[%d]: Invalid user %s from %s port %d",
			at,
			pod.name,
			pid,
			pick(rng, []string{"admin", "test", "oracle", "ubuntu", "git"}),
			docIP(rng),
			30_000+rng.Intn(30_000),
		)
	case 3, 4:
		return "INFO", fmt.Sprintf(
			"%s %s CRON[%d]: (root) CMD (%s)",
			at,
			pod.name,
			pid,
			pick(
				rng,
				[]string{
					"/usr/local/bin/erp-export --daily",
					"test -x /usr/sbin/anacron || run-parts /etc/cron.daily",
					"/opt/backup/run.sh",
				},
			),
		)
	case 5:
		return "INFO", fmt.Sprintf(
			"%s %s systemd[1]: Started %s.",
			at,
			pod.name,
			pick(
				rng,
				[]string{
					"Daily apt download activities",
					"Session 42 of User deploy",
					"Cleanup of Temporary Directories",
				},
			),
		)
	case 6:
		return "INFO", fmt.Sprintf("%s %s systemd[1]: %s.service: Deactivated successfully.", at, pod.name,
			pick(rng, []string{"apt-daily", "logrotate", "man-db"}))
	default:
		if rng.Intn(20) == 0 {
			return "ERROR", fmt.Sprintf(
				"%s %s kernel: Out of memory: Killed process %d (java) total-vm:%dkB, anon-rss:%dkB, file-rss:0kB, shmem-rss:0kB, UID:1001 pgtables:%dkB oom_score_adj:0",
				at,
				pod.name,
				pid,
				8_000_000+rng.Intn(4_000_000),
				6_000_000+rng.Intn(2_000_000),
				10_000+rng.Intn(5000),
			)
		}
		return "INFO", fmt.Sprintf(
			"%s %s systemd-logind[%d]: New session %d of user deploy.",
			at,
			pod.name,
			pid,
			rng.Intn(5000),
		)
	}
}

// klog is a Kubernetes component's klog header: Lmmdd hh:mm:ss.uuuuuu pid file:line] msg.
func klog(level byte, ts time.Time, pid int, site, msg string) string {
	return fmt.Sprintf("%c%s %7d %s] %s", level, ts.UTC().Format("0102 15:04:05.000000"), pid, site, msg)
}

// podIP is an address in the cluster's pod range (10.42.0.0/16).
func podIP(rng *rand.Rand) string {
	return fmt.Sprintf("10.42.%d.%d", rng.Intn(clusterNodes), 2+rng.Intn(250))
}

func pick2(rng *rand.Rand, a, b int) int {
	if rng.Intn(2) == 0 {
		return a
	}
	return b
}

func pickWorkload(rng *rand.Rand, workloads []*clusterWorkload) *clusterWorkload {
	total := 0
	for _, w := range workloads {
		total += w.weight
	}
	n := rng.Intn(total)
	for _, w := range workloads {
		if n < w.weight {
			return w
		}
		n -= w.weight
	}
	return workloads[len(workloads)-1]
}

func pickContainer(rng *rand.Rand, containers []clusterContainer) int {
	total := 0
	for _, c := range containers {
		total += c.weight
	}
	n := rng.Intn(total)
	for i, c := range containers {
		if n < c.weight {
			return i
		}
		n -= c.weight
	}
	return len(containers) - 1
}

func pickLine(rng *rand.Rand, lines []weightedLine) lineFunc {
	total := 0
	for _, l := range lines {
		total += l.weight
	}
	n := rng.Intn(total)
	for _, l := range lines {
		if n < l.weight {
			return l.line
		}
		n -= l.weight
	}
	return lines[len(lines)-1].line
}
