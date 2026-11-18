package webanalytics

import (
	"fmt"
	"regexp"
	"strings"
)

// The normalized access-log schema uses OpenTelemetry semantic convention
// names in their stored form (attrnames.Normalize: client.address is
// client_address). Generated collector configs write these names; each
// field's later aliases are what web servers and older configs wrote, read
// with COALESCE at query time (first alias wins). A log line kept as plain
// text (Kubernetes container logs) is parsed with accessLineRegex, and a
// JSON body with the same aliases.
const (
	fieldClient   = "client_address"
	fieldMethod   = "http_request_method"
	fieldPath     = "url_path"
	fieldProtocol = "network_protocol_version"
	fieldStatus   = "http_response_status_code"
	fieldBytes    = "http_response_body_size"
	fieldReferer  = "http_request_header_referer"
	fieldUA       = "user_agent_original"
	fieldHost     = "server_address"
	fieldTLS      = "tls_protocol_version"
	fieldDuration = "http_server_request_duration"
)

// Alias index offsets: a field's alias index (an *_ai column) is its
// 1-based position in the fields list, aliasFromMessage+n for the JSON
// body's n-th path, or aliasFromText for the parsed text line.
const (
	aliasFromMessage = 1000
	aliasFromText    = 2000
)

// accessLineRegex parses Apache/Nginx common and combined lines, plus the
// request length and time ingress-nginx appends (RE2 syntax for DuckDB).
const accessLineRegex = `^(\S+) \S+ \S+ \[[^\]]+\] "([A-Za-z]+) (\S+)(?: (HTTP/[0-9.]+))?" (\d{3}) (\d+|-)` +
	`(?: "([^"]*)" "([^"]*)")?(?: (\d+) ([0-9.]+))?`

// accessLineGroups name accessLineRegex's groups, in order.
var accessLineGroups = []string{
	"client",
	"method",
	"path",
	"proto",
	"status",
	"bytes",
	"referer",
	"ua",
	"reqlen",
	"reqtime",
}

// botPattern marks crawlers, monitors, scanners and scripts by User-Agent.
const botPattern = `(?i)(bot|crawl|spider|slurp|bingpreview|facebookexternalhit|embedly|curl/|wget/|python-requests|` +
	`python-urllib|aiohttp|go-http-client|java/|okhttp|httpclient|libwww|scrapy|headless|phantomjs|pingdom|uptime|` +
	`monitor|probe|check_http|zgrab|masscan|nmap|nikto|sqlmap|semrush|ahrefs|mj12|petalbot|bytespider|gptbot|ccbot|` +
	`preview|validator|lighthouse)`

// durationBoundsMs are the response-time histogram's upper bounds; the last
// bucket is everything slower. p95 is interpolated within a bucket.
var durationBoundsMs = []float64{
	1,
	2,
	5,
	10,
	20,
	50,
	100,
	200,
	300,
	500,
	750,
	1000,
	1500,
	2000,
	3000,
	5000,
	10000,
	30000,
}

// defaultStaticExtensions are the "Static files" setting's default.
var defaultStaticExtensions = []string{
	"css",
	"js",
	"mjs",
	"map",
	"png",
	"jpg",
	"jpeg",
	"gif",
	"svg",
	"webp",
	"avif",
	"ico",
	"woff",
	"woff2",
	"ttf",
	"eot",
	"otf",
	"mp4",
	"webm",
	"mp3",
	"pdf",
	"txt",
	"xml",
	"json",
}

var staticExtensionPattern = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

// fieldAlias is one name a field was written under; scale converts a
// duration to milliseconds.
type fieldAlias struct {
	name  string
	scale float64
}

// accessField is one normalized field and where to read it.
type accessField struct {
	name      string
	label     string
	aliases   []fieldAlias
	textGroup string
	// textScale converts the text line's value (ingress-nginx's
	// $request_time is seconds).
	textScale float64
}

// accessFields: the order is the fields list's order (alias indexes).
var accessFields = []accessField{
	{name: fieldClient, label: "Client IP", textGroup: "client", aliases: names(
		fieldClient, "remote_addr", "downstream_remote_address", "ClientHost", "ClientAddr", "c_ip", "remote_ip",
		"http_client_ip")},
	{name: fieldMethod, label: "Method", textGroup: "method", aliases: names(
		fieldMethod, "method", "RequestMethod", "cs_method", "request_method", "http_method")},
	{name: fieldPath, label: "Path", textGroup: "path", aliases: names(
		fieldPath, "path", "RequestPath", "cs_uri_stem", "request_path", "request_uri", "http_target")},
	{name: fieldProtocol, label: "Protocol", textGroup: "proto", aliases: names(
		fieldProtocol, "protocol", "RequestProtocol", "cs_version", "http_version", "http_flavor")},
	{name: fieldStatus, label: "Status", textGroup: "status", aliases: names(
		fieldStatus, "status", "response_code", "DownstreamStatus", "sc_status", "status_code", "http_status_code",
		"http_status")},
	{name: fieldBytes, label: "Bytes", textGroup: "bytes", aliases: names(
		fieldBytes, "body_bytes_sent", "bytes_sent", "DownstreamContentSize", "sc_bytes", "response_size", "bytes")},
	{name: fieldReferer, label: "Referrer", textGroup: "referer", aliases: names(
		fieldReferer, "referer", "referrer", "request_Referer", "cs_Referer_", "http_referer")},
	{name: fieldUA, label: "User agent", textGroup: "ua", aliases: names(
		fieldUA, "user_agent", "request_User_Agent", "cs_user_agent", "cs_User_Agent_", "http_user_agent")},
	{name: fieldHost, label: "Host", aliases: names(
		fieldHost, "http_request_header_host", "url_domain", "host", "authority", "RequestHost", "cs_host", "cs_Host_",
		"vhost", "server_name")},
	{name: fieldTLS, label: "TLS", aliases: names(
		fieldTLS, "tls_version", "ssl_protocol", "TLSVersion", "downstream_tls_version")},
	{name: fieldDuration, label: "Duration", textGroup: "reqtime", textScale: 1000, aliases: []fieldAlias{
		{fieldDuration, 1000}, {"duration_ms", 1}, {"duration", 1}, {"Duration", 1e-6}, {"time_taken", 1},
		{"request_time", 1000}, {"request_duration_ms", 1}}},
}

// identityAttributes: what the source identity rule reads (as the
// ingestion status does): obzly_source > k8s namespace + workload (+
// cluster) > service (+ namespace) + host > cluster > host.
var identityAttributes = []string{
	"obzly_source", "k8s_cluster_name", "k8s_namespace_name", "k8s_deployment_name", "k8s_statefulset_name",
	"k8s_daemonset_name", "k8s_cronjob_name", "k8s_job_name", "service_name", "service_namespace", "host_name",
}

// workloadKinds name identityAttributes' workloads, in match order.
var workloadKinds = []struct{ attr, kind string }{
	{"k8s_deployment_name", "deployment"},
	{"k8s_statefulset_name", "statefulset"},
	{"k8s_daemonset_name", "daemonset"},
	{"k8s_cronjob_name", "cronjob"},
	{"k8s_job_name", "job"},
}

// scanOptions are the settings a scan applies; they shape how an hour is
// summarised, so a change applies to hours built afterwards.
type scanOptions struct {
	ipMode           string
	salt             string
	excludeBots      bool
	staticExtensions []string
}

// sqlLayout records each field's and identity attribute's position in the
// fields list (1-based, DuckDB list indexing).
type sqlLayout struct {
	fieldPositions    map[string][]int
	messagePositions  map[string][]int
	identityPositions map[string]int
	fieldPaths        []string
	messagePaths      []string
}

func names(list ...string) []fieldAlias {
	aliases := make([]fieldAlias, len(list))
	for i, name := range list {
		aliases[i] = fieldAlias{name: name, scale: 1}
	}
	return aliases
}

func newSQLLayout() *sqlLayout {
	layout := &sqlLayout{
		fieldPositions:    map[string][]int{},
		messagePositions:  map[string][]int{},
		identityPositions: map[string]int{},
	}
	for _, field := range accessFields {
		for _, alias := range field.aliases {
			path := jsonPath(alias.name)
			layout.fieldPaths = append(layout.fieldPaths, path)
			layout.fieldPositions[field.name] = append(layout.fieldPositions[field.name], len(layout.fieldPaths))
			layout.messagePaths = append(layout.messagePaths, path)
			layout.messagePositions[field.name] = append(layout.messagePositions[field.name], len(layout.messagePaths))
		}
	}
	for _, attribute := range identityAttributes {
		layout.fieldPaths = append(layout.fieldPaths, jsonPath(attribute))
		layout.identityPositions[attribute] = len(layout.fieldPaths)
	}
	return layout
}

// aliasName names an *_ai alias index: the attribute, "message JSON: x" or
// "access-log line"; "" for none.
func aliasName(fieldName string, index int) string {
	field := fieldByName(fieldName)
	if field == nil || index <= 0 {
		return ""
	}
	switch {
	case index >= aliasFromText:
		return "access-log line"
	case index >= aliasFromMessage:
		position := index - aliasFromMessage
		if position >= 1 && position <= len(field.aliases) {
			return "message JSON: " + field.aliases[position-1].name
		}
		return ""
	}
	if index <= len(field.aliases) {
		return field.aliases[index-1].name
	}
	return ""
}

func fieldByName(name string) *accessField {
	for i := range accessFields {
		if accessFields[i].name == name {
			return &accessFields[i]
		}
	}
	return nil
}

// sanitizeStaticExtensions keeps lowercase alphanumeric extensions (they are
// spliced into SQL), without dots, deduplicated.
func sanitizeStaticExtensions(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for part := range strings.FieldsFuncSeq(strings.ToLower(raw), func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '\n' || r == '\t'
	}) {
		part = strings.TrimPrefix(part, ".")
		if staticExtensionPattern.MatchString(part) && !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// tableSQL is the trusted SELECT over "raw" that materializes the
// normalized access logs (one row per request), with its bind arguments.
// Only rows with a status and a method or path count as access logs.
func tableSQL(opts scanOptions) (string, []any) {
	layout := newSQLLayout()
	fieldList := sqlStringList(layout.fieldPaths)
	messageList := sqlStringList(layout.messagePaths)

	// DuckDB may evaluate a CASE branch on rows the condition excludes (seen
	// over thousands of non-JSON lines), so the JSON argument itself is
	// guarded too: json_extract_string fails on anything that isn't JSON.
	jsonMessage := "CASE WHEN starts_with(message, '{') AND json_valid(message) THEN message END"
	level1 := "SELECT CAST(timestamp AS TIMESTAMP) AS ts, message, " +
		"json_extract_string(CASE WHEN json_valid(fields) THEN fields END, " + fieldList + ") AS fv, " +
		"json_extract_string(" + jsonMessage + ", " + messageList + ") AS mv FROM raw"

	statusKnown := []string{}
	for _, position := range layout.fieldPositions[fieldStatus] {
		statusKnown = append(statusKnown, fmt.Sprintf("fv[%d]", position))
	}
	for _, position := range layout.messagePositions[fieldStatus] {
		statusKnown = append(statusKnown, fmt.Sprintf("mv[%d]", position))
	}
	level2 := "SELECT ts, fv, mv, CASE WHEN COALESCE(" + strings.Join(statusKnown, ", ") + ") IS NULL " +
		`AND message LIKE '%] "%' THEN regexp_extract(message, ` + sqlString(accessLineRegex) + ", " +
		sqlStringList(accessLineGroups) + ") END AS rx FROM (" + level1 + ") l1"

	var level3 []string
	level3 = append(level3, "ts")
	for _, field := range accessFields {
		level3 = append(level3, fieldValueSQL(layout, field)+" AS "+field.name+"_raw")
		level3 = append(level3, aliasIndexSQL(layout, field)+" AS "+field.name+"_ai")
	}
	for _, attribute := range identityAttributes {
		level3 = append(
			level3,
			fmt.Sprintf("NULLIF(fv[%d], '') AS id_%s", layout.identityPositions[attribute], attribute),
		)
	}
	level3SQL := "SELECT " + strings.Join(level3, ", ") + " FROM (" + level2 + ") l2"

	// The first address of an X-Forwarded-For style list, without a port.
	rawClient := "trim(split_part(" + fieldClient + "_raw, ',', 1))"
	clientSQL := "lower(CASE " +
		"WHEN regexp_matches(" + rawClient + `, '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+$') THEN split_part(` + rawClient + ", ':', 1) " +
		"WHEN regexp_matches(" + rawClient + `, '^\[.*\]:[0-9]+$') THEN regexp_extract(` + rawClient + `, '^\[(.*)\]', 1) ` +
		"WHEN regexp_matches(" + rawClient + `, '^::ffff:[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$') THEN substr(` + rawClient + ", 8) " +
		"ELSE " + rawClient + " END)"

	level4 := []string{
		"ts",
		identitySQL() + " AS src",
		"coalesce(lower(regexp_replace(trim(" + fieldHost + "_raw), ':[0-9]+$', '')), '') AS host",
		clientSQL + " AS client_c",
		"upper(trim(" + fieldMethod + "_raw)) AS method",
		"NULLIF(split_part(" + fieldPath + "_raw, '?', 1), '') AS path",
		protocolSQL(fieldProtocol+"_raw") + " AS proto",
		"TRY_CAST(" + fieldStatus + "_raw AS INTEGER) AS status",
		"TRY_CAST(" + fieldBytes + "_raw AS BIGINT) AS bytes",
		refererHostSQL(fieldReferer+"_raw") + " AS ref_host",
		"left(" + fieldUA + "_raw, 512) AS ua",
		tlsSQL(fieldTLS+"_raw") + " AS tls",
		fieldDuration + "_raw AS dur",
		"id_service_name AS id_service", "id_host_name AS id_hostname", "id_k8s_cluster_name AS id_cluster",
		"id_k8s_namespace_name AS id_namespace",
	}
	for _, field := range accessFields {
		level4 = append(level4, field.name+"_ai AS ai_"+shortName(field.name))
	}
	level4SQL := "SELECT " + strings.Join(level4, ", ") + " FROM (" + level3SQL + ") l3"

	ipv4 := `regexp_matches(client_c, '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$')`
	ipv6 := `regexp_matches(client_c, '^[0-9a-f:]+$') AND contains(client_c, ':')`
	prefixSQL := "CASE WHEN " + ipv4 + ` THEN regexp_replace(client_c, '\.[0-9]+$', '.0') || '/24' ` +
		"WHEN " + ipv6 + ` THEN regexp_replace(array_to_string(list_resize(string_split(split_part(client_c, '::', 1), ':'), 3, '0'), ':'), '^:', '0:') || '::/48' END`
	ipKeySQL := prefixSQL
	var args []any
	switch opts.ipMode {
	case ipModeFull:
		ipKeySQL = "client"
	case ipModeHash:
		ipKeySQL = "CASE WHEN client IS NOT NULL THEN 'ip#' || left(sha256(? || client), 12) END"
		args = append(args, opts.salt)
	}
	staticSQL := "false"
	if len(opts.staticExtensions) > 0 {
		staticSQL = "path IS NOT NULL AND regexp_matches(lower(path), " +
			sqlString(`\.(`+strings.Join(opts.staticExtensions, "|")+`)$`) + ")"
	}
	botSQL := "ua IS NOT NULL AND (trim(ua) IN ('', '-') OR regexp_matches(ua, " + sqlString(botPattern) + "))"
	excludeBots := "false"
	if opts.excludeBots {
		excludeBots = "true"
	}

	level5 := []string{
		"*",
		prefixSQL + " AS prefix",
		staticSQL + " AS is_static",
		botSQL + " AS bot",
	}
	level5SQL := "SELECT " + strings.Join(level5, ", ") + " FROM (SELECT *, CASE WHEN " + ipv4 + " OR (" + ipv6 +
		") THEN client_c END AS client FROM (" + level4SQL + ") l4) l4b"

	final := []string{
		"ts",
		"src",
		"host",
		"client",
		ipKeySQL + " AS ipkey",
		"prefix",
		"method",
		"path",
		"CASE WHEN path IS NULL OR is_static THEN NULL ELSE " + pathTemplateSQL("path") + " END AS page",
		"is_static",
		"proto",
		"status",
		"bytes",
		"ref_host",
		"ua",
		"bot",
		"CASE WHEN client IS NOT NULL AND NOT (bot AND " + excludeBots + ") THEN hash(client || '|' || coalesce(ua, '')) END AS vhash",
		"(bot AND " + excludeBots + ") AS hidden",
		"tls",
		"dur",
		"id_service",
		"id_hostname",
		"id_cluster",
		"id_namespace",
	}
	for _, field := range accessFields {
		final = append(final, "ai_"+shortName(field.name))
	}
	return "SELECT " + strings.Join(final, ", ") + " FROM (" + level5SQL + ") l5 " +
		"WHERE status BETWEEN 100 AND 599 AND (method IS NOT NULL OR path IS NOT NULL)", args
}

// fieldValueSQL is a field's value: the first alias found in the fields,
// then in a JSON body, then in the parsed line. "" and "-" count as
// missing except for the referrer and user agent, where "-" means the
// field is logged but empty.
func fieldValueSQL(layout *sqlLayout, field accessField) string {
	keepDash := field.name == fieldReferer || field.name == fieldUA
	value := func(expr string) string {
		if keepDash {
			return "NULLIF(" + expr + ", '')"
		}
		return "NULLIF(NULLIF(" + expr + ", ''), '-')"
	}
	var parts []string
	if field.name == fieldDuration {
		for i, position := range layout.fieldPositions[field.name] {
			parts = append(parts, fmt.Sprintf("TRY_CAST(%s AS DOUBLE) * %g", value(fmt.Sprintf("fv[%d]", position)),
				field.aliases[i].scale))
		}
		for i, position := range layout.messagePositions[field.name] {
			parts = append(parts, fmt.Sprintf("TRY_CAST(%s AS DOUBLE) * %g", value(fmt.Sprintf("mv[%d]", position)),
				field.aliases[i].scale))
		}
		parts = append(
			parts,
			fmt.Sprintf("TRY_CAST(NULLIF(rx.%s, '') AS DOUBLE) * %g", field.textGroup, field.textScale),
		)
		return "COALESCE(" + strings.Join(parts, ", ") + ")"
	}
	for _, position := range layout.fieldPositions[field.name] {
		parts = append(parts, value(fmt.Sprintf("fv[%d]", position)))
	}
	for _, position := range layout.messagePositions[field.name] {
		parts = append(parts, value(fmt.Sprintf("mv[%d]", position)))
	}
	if field.textGroup != "" {
		parts = append(parts, "NULLIF(rx."+field.textGroup+", '')")
	}
	return "COALESCE(" + strings.Join(parts, ", ") + ")"
}

// aliasIndexSQL is which alias fieldValueSQL used (see aliasName).
func aliasIndexSQL(layout *sqlLayout, field accessField) string {
	var whens []string
	for i, position := range layout.fieldPositions[field.name] {
		whens = append(whens, fmt.Sprintf("WHEN NULLIF(fv[%d], '') IS NOT NULL THEN %d", position, i+1))
	}
	for i, position := range layout.messagePositions[field.name] {
		whens = append(
			whens,
			fmt.Sprintf("WHEN NULLIF(mv[%d], '') IS NOT NULL THEN %d", position, aliasFromMessage+i+1),
		)
	}
	if field.textGroup != "" {
		whens = append(whens, fmt.Sprintf("WHEN NULLIF(rx.%s, '') IS NOT NULL THEN %d", field.textGroup, aliasFromText))
	}
	return "CAST(CASE " + strings.Join(whens, " ") + " END AS SMALLINT)"
}

// identitySQL builds the identity key the ingestion status uses
// ("source\n<name>", "k8s\n<cluster>\n<namespace>\n<kind>\n<name>", ...).
func identitySQL() string {
	nl := " || chr(10) || "
	var workloadCase []string
	var workloadNames []string
	for _, workload := range workloadKinds {
		workloadCase = append(workloadCase, "WHEN id_"+workload.attr+" IS NOT NULL THEN '"+workload.kind+"'")
		workloadNames = append(workloadNames, "id_"+workload.attr)
	}
	workloadName := "COALESCE(" + strings.Join(workloadNames, ", ") + ")"
	return "CASE WHEN id_obzly_source IS NOT NULL THEN 'source'" + nl + "id_obzly_source " +
		"WHEN id_k8s_namespace_name IS NOT NULL AND " + workloadName + " IS NOT NULL THEN 'k8s'" + nl +
		"coalesce(id_k8s_cluster_name, '')" + nl + "id_k8s_namespace_name" + nl +
		"CASE " + strings.Join(workloadCase, " ") + " END" + nl + workloadName + " " +
		"WHEN id_service_name IS NOT NULL THEN 'service'" + nl + "coalesce(id_service_namespace, '')" + nl +
		"id_service_name" + nl + "coalesce(id_host_name, '') " +
		"WHEN id_k8s_cluster_name IS NOT NULL THEN 'cluster'" + nl + "id_k8s_cluster_name " +
		"WHEN id_host_name IS NOT NULL THEN 'host'" + nl + "id_host_name " +
		"ELSE 'unidentified' END"
}

// protocolSQL turns "1.1", "HTTP/1.1" and "HTTP/2.0" into "HTTP/1.1", "HTTP/2".
func protocolSQL(expr string) string {
	upper := "upper(trim(" + expr + "))"
	return "regexp_replace(CASE WHEN regexp_matches(" + upper + ", '^[0-9.]+$') THEN 'HTTP/' || " + upper +
		" ELSE " + upper + ` END, '^HTTP/([23])\.0$', 'HTTP/\1')`
}

// tlsSQL turns "1.3", "TLSv1.3" and "TLS 1.3" into "TLSv1.3".
func tlsSQL(expr string) string {
	trimmed := "trim(" + expr + ")"
	return "CASE WHEN regexp_matches(" + trimmed + ", '^[0-9.]+$') THEN 'TLSv' || " + trimmed +
		" ELSE regexp_replace(" + trimmed + ", '(?i)^tls ?v?', 'TLSv') END"
}

// refererHostSQL is the referrer's host: NULL when the format doesn't log a
// referrer, "(direct or none)" when it's empty.
func refererHostSQL(expr string) string {
	host := "lower(regexp_extract(" + expr + ", '^[a-zA-Z][a-zA-Z0-9+.-]*://([^/:?#]+)', 1))"
	return "CASE WHEN " + expr + " IS NULL THEN NULL WHEN trim(" + expr + ") IN ('', '-') THEN '" + directReferrer +
		"' ELSE NULLIF(" + host + ", '') END"
}

// pathTemplateSQL groups IDs in paths (numbers, UUIDs, long hex) as :id,
// as log patterns do, so /catalog/42 and /catalog/43 count together.
func pathTemplateSQL(expr string) string {
	value := expr + " || '/'"
	for _, pattern := range []string{
		`/[0-9]+/`,
		`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/`,
		`/[0-9a-fA-F]{16,}/`,
	} {
		// Twice: adjacent IDs share the slash between them.
		for range 2 {
			value = "regexp_replace(" + value + ", " + sqlString(pattern) + ", '/:id/', 'g')"
		}
	}
	return "CASE WHEN " + expr + " = '/' THEN '/' ELSE rtrim(" + value + ", '/') END"
}

func shortName(field string) string {
	switch field {
	case fieldClient:
		return "client"
	case fieldMethod:
		return "method"
	case fieldPath:
		return "path"
	case fieldProtocol:
		return "proto"
	case fieldStatus:
		return "status"
	case fieldBytes:
		return "bytes"
	case fieldReferer:
		return "referer"
	case fieldUA:
		return "ua"
	case fieldHost:
		return "host"
	case fieldTLS:
		return "tls"
	default:
		return "dur"
	}
}

func jsonPath(name string) string {
	return `$."` + strings.ReplaceAll(name, `"`, `\"`) + `"`
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func sqlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = sqlString(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
