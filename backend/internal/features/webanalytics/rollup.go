package webanalytics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Dimensions of the top lists. dimUA and dimPrefix exist only inside a scan:
// they become browsers/systems/devices and countries before anything is
// stored, so neither User-Agents nor address prefixes are kept.
const (
	dimPage     = 1
	dimStatic   = 2
	dimNotFound = 3
	dimReferrer = 4
	dimIP       = 5
	dimMethod   = 6
	dimProtocol = 7
	dimTLS      = 8
	dimStatus   = 9
	dimBrowser  = 10
	dimOS       = 11
	dimDevice   = 12
	dimCountry  = 13

	dimUA     = 100
	dimPrefix = 101
)

// Presence counters (totalsAgg.with): how many requests logged each field,
// which tells which formats lack referrers, durations or TLS.
const (
	withClient = iota
	withBytes
	withReferer
	withUA
	withDuration
	withTLS
	withHost
	withProtocol
	withCount
)

const (
	// maxHostsPerSource: hosts kept apart per source and window; the rest
	// count as otherHost (scanners send any Host header).
	maxHostsPerSource = 10
	// storedDayKeys: rows kept per list in a day's rollup.
	storedDayKeys = 200
)

// keyLimits are rows kept per list per source, host and hour.
var keyLimits = map[int]int{
	dimPage: 100, dimStatic: 100, dimNotFound: 100, dimReferrer: 100, dimIP: 100,
	dimMethod: 20, dimProtocol: 20, dimTLS: 20, dimStatus: 60,
	dimBrowser: 50, dimOS: 50, dimDevice: 10, dimCountry: 250,
}

// sketchDims are the lists with a Visitors column.
var sketchDims = map[int]bool{
	dimPage: true, dimNotFound: true, dimReferrer: true, dimBrowser: true, dimOS: true, dimDevice: true,
	dimCountry: true,
}

// totalsAgg is one source and host's counts for a bucket.
type totalsAgg struct {
	requests, bytes        int64
	s2, s3, s4, s5, sOther int64
	notFound, bots         int64
	with                   [withCount]int64
	hist                   []int64
	visitors               *sketch
	// format is the encoded fieldMap of most of the requests
	// (formatWeight of them).
	format                     string
	formatWeight               int64
	service, hostName, cluster string
	namespace                  string
}

// keyAgg is one row of a top list.
type keyAgg struct {
	hits, bytes, e5, referred int64
	lastSeen                  time.Time
	extra                     string
	visitors                  *sketch
}

type srcHost struct{ src, host string }

type bucketKey struct {
	bucket time.Time
	src    string
	host   string
}

// keySet is a window's top lists by dimension and key.
type keySet map[int]map[string]*keyAgg

// rollup is what a scan or a set of stored rows says about a window:
// counts per bucket, and top lists for the whole window.
type rollup struct {
	totals   map[bucketKey]*totalsAgg
	keys     map[srcHost]keySet
	rowsRead int64
}

// fieldMap is a source's format: the dialect and each field's alias.
type fieldMap struct {
	Dialect string            `json:"dialect"`
	Fields  map[string]string `json:"fields"`
}

func newRollup() *rollup {
	return &rollup{totals: map[bucketKey]*totalsAgg{}, keys: map[srcHost]keySet{}}
}

func newTotals() *totalsAgg {
	return &totalsAgg{hist: make([]int64, len(durationBoundsMs)+1), visitors: newSketch(totalPrecision)}
}

func (t *totalsAgg) merge(other *totalsAgg) {
	t.requests += other.requests
	t.bytes += other.bytes
	t.s2 += other.s2
	t.s3 += other.s3
	t.s4 += other.s4
	t.s5 += other.s5
	t.sOther += other.sOther
	t.notFound += other.notFound
	t.bots += other.bots
	for i := range t.with {
		t.with[i] += other.with[i]
	}
	for i := range t.hist {
		if i < len(other.hist) {
			t.hist[i] += other.hist[i]
		}
	}
	t.visitors.merge(other.visitors)
	// A source's format is that of most of its requests.
	if other.formatWeight > t.formatWeight {
		t.format = other.format
		t.formatWeight = other.formatWeight
	}
	t.service = firstNonEmpty(t.service, other.service)
	t.hostName = firstNonEmpty(t.hostName, other.hostName)
	t.cluster = firstNonEmpty(t.cluster, other.cluster)
	t.namespace = firstNonEmpty(t.namespace, other.namespace)
}

func (k *keyAgg) merge(other *keyAgg) {
	k.hits += other.hits
	k.bytes += other.bytes
	k.e5 += other.e5
	k.referred += other.referred
	if other.lastSeen.After(k.lastSeen) {
		k.lastSeen = other.lastSeen
	}
	k.extra = firstNonEmpty(k.extra, other.extra)
	if other.visitors != nil {
		if k.visitors == nil {
			k.visitors = newSketch(other.visitors.p)
		}
		k.visitors.merge(other.visitors)
	}
}

func (s keySet) add(dim int, key string, agg *keyAgg) {
	list := s[dim]
	if list == nil {
		list = map[string]*keyAgg{}
		s[dim] = list
	}
	if existing := list[key]; existing != nil {
		existing.merge(agg)
		return
	}
	copied := *agg
	if agg.visitors != nil {
		copied.visitors = newSketch(agg.visitors.p)
		copied.visitors.merge(agg.visitors)
	}
	list[key] = &copied
}

// truncate keeps each list's limit most-hit rows.
func (s keySet) truncate(limitFor func(dim int) int) {
	for dim, list := range s {
		limit := limitFor(dim)
		if limit <= 0 || len(list) <= limit {
			continue
		}
		for _, key := range topKeys(list, len(list))[limit:] {
			delete(list, key)
		}
	}
}

// topKeys is list's keys, most hits first (ties by key), at most n.
func topKeys(list map[string]*keyAgg, n int) []string {
	keys := make([]string, 0, len(list))
	for key := range list {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := list[keys[i]], list[keys[j]]
		if a.hits != b.hits {
			return a.hits > b.hits
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// finish turns a scan's raw lists into stored ones: User-Agents into
// browsers, systems and devices, address prefixes into countries (none
// without a database), and hosts past maxHostsPerSource into otherHost.
func (r *rollup) finish(geo *countryDB, excludeBots bool) {
	for sh, set := range r.keys {
		if uas := set[dimUA]; uas != nil {
			for ua, agg := range uas {
				bot := agg.extra == "true"
				if bot && excludeBots {
					continue
				}
				parsed := parseAgent(ua, bot)
				set.add(dimBrowser, parsed.browser, stripExtra(agg))
				set.add(dimOS, parsed.os, stripExtra(agg))
				set.add(dimDevice, parsed.device, stripExtra(agg))
			}
			delete(set, dimUA)
		}
		if prefixes := set[dimPrefix]; prefixes != nil {
			for prefix, agg := range prefixes {
				if country := geo.lookup(prefix); country != "" {
					set.add(dimCountry, country, stripExtra(agg))
				}
			}
			delete(set, dimPrefix)
		}
		if ips := set[dimIP]; ips != nil {
			for _, agg := range ips {
				agg.extra = geo.lookup(agg.extra)
			}
		}
		r.keys[sh] = set
	}
	r.foldHosts()
	for _, set := range r.keys {
		set.truncate(func(dim int) int { return keyLimits[dim] })
	}
}

// foldHosts merges each source's hosts past its maxHostsPerSource busiest
// (over the whole window) into otherHost.
func (r *rollup) foldHosts() {
	requests := map[string]map[string]int64{}
	for key, totals := range r.totals {
		if requests[key.src] == nil {
			requests[key.src] = map[string]int64{}
		}
		requests[key.src][key.host] += totals.requests
	}
	keep := map[srcHost]bool{}
	for src, hosts := range requests {
		names := make([]string, 0, len(hosts))
		for host := range hosts {
			names = append(names, host)
		}
		sort.Slice(names, func(i, j int) bool {
			if hosts[names[i]] != hosts[names[j]] {
				return hosts[names[i]] > hosts[names[j]]
			}
			return names[i] < names[j]
		})
		for i, host := range names {
			if i < maxHostsPerSource || host == "" {
				keep[srcHost{src, host}] = true
			}
		}
	}
	for key, totals := range r.totals {
		if keep[srcHost{key.src, key.host}] {
			continue
		}
		delete(r.totals, key)
		folded := bucketKey{key.bucket, key.src, otherHost}
		if existing := r.totals[folded]; existing != nil {
			existing.merge(totals)
		} else {
			r.totals[folded] = totals
		}
	}
	for sh, set := range r.keys {
		if keep[sh] {
			continue
		}
		delete(r.keys, sh)
		folded := srcHost{sh.src, otherHost}
		target := r.keys[folded]
		if target == nil {
			target = keySet{}
			r.keys[folded] = target
		}
		for dim, list := range set {
			for key, agg := range list {
				target.add(dim, key, agg)
			}
		}
	}
}

// formatOf is the source format a totals row's aliases show.
func formatOf(aliases map[string]int) fieldMap {
	fields := map[string]string{}
	for name, index := range aliases {
		if alias := aliasName(name, index); alias != "" {
			fields[name] = alias
		}
	}
	return fieldMap{Dialect: dialectOf(aliases), Fields: fields}
}

// dialectOf names the log format from which aliases a source's requests
// used: the status field's name is distinct per server.
func dialectOf(aliases map[string]int) string {
	status := aliases[fieldStatus]
	switch {
	case status >= aliasFromText:
		if aliases[fieldUA] >= aliasFromText {
			return "combined (text)"
		}
		return "common (text)"
	case status >= aliasFromMessage:
		return "JSON in message"
	}
	switch aliasName(fieldStatus, status) {
	case fieldStatus:
		return "OTel names"
	case "response_code":
		return "JSON (Envoy)"
	case "DownstreamStatus":
		return "JSON (Traefik)"
	case "sc_status":
		return "W3C (IIS)"
	case "status":
		switch aliasName(fieldBytes, aliases[fieldBytes]) {
		case "body_bytes_sent":
			return "combined"
		case "bytes_sent":
			if aliases[fieldUA] == 0 {
				return "common"
			}
			return "combined"
		}
	}
	if status == 0 {
		return ""
	}
	return "fields"
}

func encodeFormat(aliases map[string]int) string {
	data, err := json.Marshal(formatOf(aliases))
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeFormat(text string) fieldMap {
	var format fieldMap
	if text != "" {
		_ = json.Unmarshal([]byte(text), &format)
	}
	if format.Fields == nil {
		format.Fields = map[string]string{}
	}
	return format
}

// sourceKey is the stored id of an identity key (as the ingestion status
// stores it, so both name a source the same).
func sourceKey(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}

// identityParts describes an identity key: how it was identified, the
// attributes it came from (in display order) and a default display name.
func identityParts(identity string) (by string, attributes [][2]string, name string) {
	parts := strings.Split(identity, "\n")
	switch parts[0] {
	case "source":
		if len(parts) >= 2 {
			return "obzly_source", [][2]string{{"obzly_source", parts[1]}}, parts[1]
		}
	case "k8s":
		if len(parts) >= 5 {
			attributes = [][2]string{}
			if parts[1] != "" {
				attributes = append(attributes, [2]string{"k8s_cluster_name", parts[1]})
			}
			attributes = append(attributes, [2]string{"k8s_namespace_name", parts[2]},
				[2]string{"k8s_" + parts[3] + "_name", parts[4]})
			return "k8s_workload", attributes, parts[4]
		}
	case "service":
		if len(parts) >= 4 {
			attributes = [][2]string{{"service_name", parts[2]}}
			if parts[1] != "" {
				attributes = append(attributes, [2]string{"service_namespace", parts[1]})
			}
			name = parts[2]
			if parts[3] != "" {
				attributes = append(attributes, [2]string{"host_name", parts[3]})
				name += "@" + parts[3]
			}
			return "service", attributes, name
		}
	case "cluster":
		if len(parts) >= 2 {
			return "k8s_cluster", [][2]string{{"k8s_cluster_name", parts[1]}}, parts[1]
		}
	case "host":
		if len(parts) >= 2 {
			return "host", [][2]string{{"host_name", parts[1]}}, parts[1]
		}
	}
	return "unidentified", [][2]string{}, "unidentified"
}

func stripExtra(agg *keyAgg) *keyAgg {
	copied := *agg
	copied.extra = ""
	return &copied
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
