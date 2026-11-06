// Package attrnames is the one naming rule for attribute, label and metric
// names: PromQL-safe, no dots, no "resource." prefix (ARCHITECTURE.md
// "Attribute naming"). frontend/src/shared/lib/attributeNames.ts mirrors it.
package attrnames

import (
	"sort"
	"strings"
)

const resourcePrefix = "resource."

// Normalize drops a leading "resource.", turns every character outside
// [a-zA-Z0-9_] into "_" and prefixes "_" when the name starts with a digit.
// Idempotent.
func Normalize(key string) string {
	return sanitize(strings.TrimPrefix(key, resourcePrefix))
}

// NormalizeMetricName applies the character rule to a metric name.
func NormalizeMetricName(name string) string {
	return sanitize(name)
}

// IsNormal reports whether key is already in normal form.
func IsNormal(key string) bool {
	return Normalize(key) == key
}

// NormalizeMap renames every key. When several keys map to one name, the
// most specific source wins: a record attribute over a "scope." one over a
// "resource." one, and an already-normal key over a converted one. Values
// are kept as they are.
func NormalizeMap(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return fields
	}
	type candidate struct {
		key  string
		rank int
	}
	winners := make(map[string]candidate, len(fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys) // deterministic tie-break

	for _, key := range keys {
		name := Normalize(key)
		rank := sourceRank(key)
		if current, ok := winners[name]; !ok || rank > current.rank {
			winners[name] = candidate{key, rank}
		}
	}

	out := make(map[string]any, len(winners))
	for name, winner := range winners {
		out[name] = fields[winner.key]
	}
	return out
}

// NormalizeStringMap is NormalizeMap for string values (metric labels), with NUL bytes
// dropped: Postgres text (the label index) rejects them and would fail the whole batch.
func NormalizeStringMap(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return labels
	}
	generic := make(map[string]any, len(labels))
	for k, v := range labels {
		generic[k] = strings.ReplaceAll(v, "\x00", "")
	}
	out := make(map[string]string, len(labels))
	for k, v := range NormalizeMap(generic) {
		out[k], _ = v.(string)
	}
	return out
}

func sourceRank(key string) int {
	rank := 4 // record attribute
	switch {
	case strings.HasPrefix(key, resourcePrefix):
		rank = 0
	case strings.HasPrefix(key, "scope."):
		rank = 2
	}
	if IsNormal(key) {
		rank++
	}
	return rank
}

func sanitize(name string) string {
	if name == "" {
		return name
	}
	var b strings.Builder
	b.Grow(len(name) + 1)
	for i, r := range name {
		valid := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && r >= '0' && r <= '9' {
			b.WriteByte('_')
		}
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
