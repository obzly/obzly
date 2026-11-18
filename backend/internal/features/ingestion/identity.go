package ingestion

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// How a source was identified, first match wins (SourceStatusDTO.IdentifiedBy).
const (
	IdentifiedByExplicit = "obzly_source"
	IdentifiedByWorkload = "k8s_workload"
	IdentifiedByService  = "service"
	IdentifiedByCluster  = "k8s_cluster"
	IdentifiedByHost     = "host"
	IdentifiedByUnknown  = "unidentified"
)

// Attribute names (normalized, see attrnames.Normalize) the status reads.
const (
	attrSource           = "obzly_source"
	attrDataSource       = "obzly_data_source"
	attrServiceName      = "service_name"
	attrServiceNamespace = "service_namespace"
	attrHostName         = "host_name"
	attrCluster          = "k8s_cluster_name"
	attrNamespace        = "k8s_namespace_name"
	attrPod              = "k8s_pod_name"
	attrNode             = "k8s_node_name"
	attrContainerID      = "container_id"
	attrContainerName    = "container_name"
	attrSDKLanguage      = "telemetry_sdk_language"
	attrOSType           = "os_type"
	attrScopeName        = "scope_name"
	// attrWorkloadKind / attrWorkload: the stored form of whichever
	// workloadFields matched.
	attrWorkloadKind = "workload_kind"
	attrWorkload     = "workload"
)

// Data source ranks: a source keeps its most specific type across signals
// and replicas.
const (
	rankHost     = 1
	rankPlatform = 2
	rankRuntime  = 3
	rankSpecific = 4
	rankExplicit = 5
)

// workloadFields in match order: a CronJob's runs are Jobs with a new name
// each time, so the CronJob is the stable identity.
var workloadFields = []struct{ field, kind string }{
	{"k8s_deployment_name", "deployment"},
	{"k8s_statefulset_name", "statefulset"},
	{"k8s_daemonset_name", "daemonset"},
	{"k8s_cronjob_name", "cronjob"},
	{"k8s_job_name", "job"},
}

// serviceNameKinds: the service.name the generated collector configs stamp
// (frontend dataSources/*DataSource.ts) and common aliases, to catalog ids.
var serviceNameKinds = map[string]string{
	"nginx": "nginx", "apache": "apache", "httpd": "apache", "postgresql": "postgresql",
	"postgres": "postgresql", "mysql": "mysql", "mariadb": "mysql", "redis": "redis",
	"valkey": "valkey", "mongodb": "mongodb", "rabbitmq": "rabbitmq", "kafka": "kafka",
	"elasticsearch": "elasticsearch", "haproxy": "haproxy", "memcached": "memcached",
	"zookeeper": "zookeeper", "couchdb": "couchdb", "riak": "riak", "sqlserver": "sqlserver",
	"iis": "iis", "vcenter": "vcenter", "cassandra": "cassandra", "consul": "consul",
	"etcd": "etcd", "envoy": "envoy", "traefik": "traefik", "flink": "flink", "spark": "spark",
	"claude-code": "claude-code",
}

// receiverKinds: OTel Collector receivers (from the instrumentation scope
// name, ".../receiver/nginxreceiver") to catalog ids and ranks.
var receiverKinds = map[string]kindHint{
	"nginx": {"nginx", rankSpecific}, "apache": {"apache", rankSpecific},
	"postgresql": {"postgresql", rankSpecific}, "mysql": {"mysql", rankSpecific},
	"redis": {"redis", rankSpecific}, "mongodb": {"mongodb", rankSpecific},
	"rabbitmq": {"rabbitmq", rankSpecific}, "kafkametrics": {"kafka", rankSpecific},
	"elasticsearch": {"elasticsearch", rankSpecific}, "haproxy": {"haproxy", rankSpecific},
	"memcached": {"memcached", rankSpecific}, "zookeeper": {"zookeeper", rankSpecific},
	"couchdb": {"couchdb", rankSpecific}, "riak": {"riak", rankSpecific},
	"sqlserver": {"sqlserver", rankSpecific}, "iis": {"iis", rankSpecific},
	"vcenter": {"vcenter", rankSpecific}, "snmp": {"snmp", rankSpecific},
	"httpcheck": {"http-check", rankSpecific}, "flinkmetrics": {"flink", rankSpecific},
	"apachespark": {"spark", rankSpecific}, "jmx": {"jvm", rankSpecific},
	"statsd": {"statsd", rankSpecific}, "syslog": {"syslog", rankSpecific},
	"prometheus":      {"prometheus", rankRuntime},
	"dockerstats":     {"docker", rankPlatform},
	"kubeletstats":    {"kubernetes", rankPlatform},
	"k8scluster":      {"kubernetes", rankPlatform},
	"k8sevents":       {"kubernetes", rankPlatform},
	"k8sobjects":      {"kubernetes", rankPlatform},
	"journald":        {"linux", rankHost},
	"windowseventlog": {"windows", rankHost},
	"hostmetrics":     {"", rankHost}, // linux or windows, by os_type
}

// metricPrefixKinds: a metric name's first word to catalog ids and ranks.
// jvm_ ranks below an SDK's language: the Java SDK reports jvm_* too.
var metricPrefixKinds = map[string]kindHint{
	"nginx": {"nginx", rankSpecific}, "apache": {"apache", rankSpecific},
	"postgresql": {"postgresql", rankSpecific}, "mysql": {"mysql", rankSpecific},
	"redis": {"redis", rankSpecific}, "mongodb": {"mongodb", rankSpecific},
	"rabbitmq": {"rabbitmq", rankSpecific}, "kafka": {"kafka", rankSpecific},
	"elasticsearch": {"elasticsearch", rankSpecific}, "haproxy": {"haproxy", rankSpecific},
	"memcached": {"memcached", rankSpecific}, "zookeeper": {"zookeeper", rankSpecific},
	"couchdb": {"couchdb", rankSpecific}, "riak": {"riak", rankSpecific},
	"sqlserver": {"sqlserver", rankSpecific}, "iis": {"iis", rankSpecific},
	"vcenter": {"vcenter", rankSpecific}, "httpcheck": {"http-check", rankSpecific},
	"flink": {"flink", rankSpecific}, "spark": {"spark", rankSpecific},
	"consul": {"consul", rankSpecific}, "etcd": {"etcd", rankSpecific},
	"envoy": {"envoy", rankSpecific}, "traefik": {"traefik", rankSpecific},
	"cassandra": {"cassandra", rankSpecific}, "claude": {"claude-code", rankSpecific},
	"gen":       {"llm-app-otel", rankRuntime},
	"jvm":       {"jvm", rankPlatform},
	"container": {"docker", rankPlatform},
	"k8s":       {"kubernetes", rankPlatform},
	"system":    {"", rankHost},
	"process":   {"", rankHost},
}

// sdkLanguageKinds: telemetry.sdk.language to the catalog's language SDKs.
var sdkLanguageKinds = map[string]string{
	"java": "lang-java", "python": "lang-python", "nodejs": "lang-nodejs", "go": "lang-go",
	"dotnet": "lang-dotnet", "ruby": "lang-ruby", "php": "lang-php", "rust": "lang-rust",
	"cpp": "lang-cpp", "erlang": "lang-erlang", "swift": "lang-swift", "webjs": "lang-browser-js",
}

// attributeGetter returns a normalized attribute's value, "" when absent.
type attributeGetter func(name string) string

// kindHint is a catalog data source id and how sure the match is.
type kindHint struct {
	kind string
	rank int
}

// sourceIdentity is who sent a record: key is stable for the same source.
type sourceIdentity struct {
	key string
	by  string
}

// identify applies the identity rule: obzly_source > k8s namespace +
// workload (+ cluster) > service (+ namespace) + host > k8s cluster > host.
func identify(get attributeGetter) sourceIdentity {
	if source := get(attrSource); source != "" {
		return sourceIdentity{"source\n" + source, IdentifiedByExplicit}
	}
	if namespace := get(attrNamespace); namespace != "" {
		for _, workload := range workloadFields {
			if name := get(workload.field); name != "" {
				return sourceIdentity{
					"k8s\n" + get(attrCluster) + "\n" + namespace + "\n" + workload.kind + "\n" + name,
					IdentifiedByWorkload,
				}
			}
		}
	}
	if service := get(attrServiceName); service != "" {
		return sourceIdentity{
			"service\n" + get(attrServiceNamespace) + "\n" + service + "\n" + get(attrHostName),
			IdentifiedByService,
		}
	}
	if cluster := get(attrCluster); cluster != "" {
		return sourceIdentity{"cluster\n" + cluster, IdentifiedByCluster}
	}
	if host := get(attrHostName); host != "" {
		return sourceIdentity{"host\n" + host, IdentifiedByHost}
	}
	return sourceIdentity{"unidentified", IdentifiedByUnknown}
}

// identityAttributes are what the status shows for a source: the values its
// identity came from plus stable descriptive ones.
func identityAttributes(get attributeGetter, by string) map[string]string {
	attributes := map[string]string{}
	set := func(name, value string) {
		if value != "" {
			attributes[name] = strings.ReplaceAll(value, "\x00", "") // Postgres text can't hold NUL
		}
	}
	set(attrSource, get(attrSource))
	set(attrSDKLanguage, get(attrSDKLanguage))
	switch by {
	case IdentifiedByExplicit, IdentifiedByWorkload:
		set(attrCluster, get(attrCluster))
		set(attrNamespace, get(attrNamespace))
		for _, workload := range workloadFields {
			if name := get(workload.field); name != "" {
				set(attrWorkloadKind, workload.kind)
				set(attrWorkload, name)
				break
			}
		}
		set(attrServiceName, get(attrServiceName))
		set(attrServiceNamespace, get(attrServiceNamespace))
		if by == IdentifiedByExplicit {
			set(attrHostName, get(attrHostName))
		}
	case IdentifiedByService:
		set(attrServiceName, get(attrServiceName))
		set(attrServiceNamespace, get(attrServiceNamespace))
		set(attrHostName, get(attrHostName))
	case IdentifiedByCluster:
		set(attrCluster, get(attrCluster))
	case IdentifiedByHost:
		set(attrHostName, get(attrHostName))
		set(attrOSType, get(attrOSType))
	}
	return attributes
}

// detectKind names the catalog data source a record most likely came from,
// from the attributes the generated configs and SDKs set: an explicit
// obzly_data_source, the service.name a collector config stamps, the
// collector receiver (scope name), the SDK language, then platform markers.
// metricName is "" for logs and spans.
func detectKind(get attributeGetter, scopeName, metricName string) kindHint {
	if explicit := get(attrDataSource); explicit != "" {
		return kindHint{explicit, rankExplicit}
	}
	best := kindHint{}
	consider := func(hint kindHint) {
		if hint.rank > best.rank {
			best = hint
		}
	}
	if kind, ok := serviceNameKinds[strings.ToLower(get(attrServiceName))]; ok {
		return kindHint{kind, rankSpecific}
	}
	if receiver := receiverOf(scopeName); receiver != "" {
		if hint, ok := receiverKinds[receiver]; ok {
			consider(hostKind(hint, get))
		}
	}
	if metricName != "" {
		prefix, _, _ := strings.Cut(metricName, "_")
		if hint, ok := metricPrefixKinds[prefix]; ok {
			consider(hostKind(hint, get))
		}
	}
	if best.rank >= rankSpecific {
		return best
	}
	if kind, ok := sdkLanguageKinds[get(attrSDKLanguage)]; ok {
		consider(kindHint{kind, rankRuntime})
	}
	if get(attrNamespace) != "" || get(attrPod) != "" || get(attrNode) != "" || get(attrCluster) != "" {
		consider(kindHint{"kubernetes", rankPlatform})
	} else if get(attrContainerID) != "" || get(attrContainerName) != "" {
		consider(kindHint{"docker", rankPlatform})
	}
	return best
}

// sourceKey is the stored, stable id of an identity key.
func sourceKey(identityKey string) string {
	sum := sha256.Sum256([]byte(identityKey))
	return hex.EncodeToString(sum[:16])
}

// receiverOf is the collector receiver in an instrumentation scope name
// (".../receiver/hostmetricsreceiver/internal/scraper/cpuscraper" is
// "hostmetrics"), "" for anything else.
func receiverOf(scopeName string) string {
	if !strings.Contains(scopeName, "receiver") {
		return ""
	}
	for part := range strings.SplitSeq(scopeName, "/") {
		if name, ok := strings.CutSuffix(part, "receiver"); ok && name != "" {
			return name
		}
	}
	return ""
}

// hostKind resolves a host-level hint (empty kind) to linux or windows.
func hostKind(hint kindHint, get attributeGetter) kindHint {
	if hint.kind != "" {
		return hint
	}
	if strings.EqualFold(get(attrOSType), "windows") {
		return kindHint{"windows", hint.rank}
	}
	return kindHint{"linux", hint.rank}
}
