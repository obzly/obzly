package logs_core

import "time"

// Group fields derived from k8s_pod_name, for grouped counts only
// (GroupWindowsRequestDTO): a pod's workload (its name without the generated
// parts) and a Deployment pod's ReplicaSet ("" for any other pod).
const (
	WorkloadField   = "k8s_workload"
	ReplicaSetField = "k8s_replicaset"
)

// Kubernetes generated name parts, shared by Go and the SQL that groups by
// WorkloadField: a pod suffix or pod-template-hash is 5-10 characters of the
// alphabet Kubernetes uses for them, which has no vowels - so "indexer"
// isn't one. A StatefulSet pod ends in its ordinal.
const (
	GeneratedNamePartPattern  = `-[bcdfghjklmnpqrstvwxz2456789]{5,10}$`
	StatefulSetOrdinalPattern = `-[0-9]+$`
)

// GroupWindowsRequestDTO counts the logs matching Query per group key in
// each of Windows. The merge picks the keys each selection wants in SQL, so
// only those reach the caller however many groups there are.
type GroupWindowsRequestDTO struct {
	Query *QueryNode
	// GroupField: fields joined by JoinGroupFields (WorkloadField and
	// ReplicaSetField too); "" counts every log under the key "".
	GroupField string
	// GroupSets, instead of GroupField, counts by each field on its own: a
	// key is the field's index in GroupSets and its value, joined by
	// GroupKeySeparator. Totals then count each log once.
	GroupSets []string
	Windows   []CountWindowDTO
	// Keys, when set, counts only these group keys.
	Keys       []string
	Selections []GroupSelectionDTO
	ScanBudget int64
}

// CountWindowDTO is one count per key: the logs in [From, To], only those
// at one of Levels when set.
type CountWindowDTO struct {
	From, To time.Time
	Levels   []string
}

// GroupSelectionDTO picks up to Limit keys passing Where, ordered by OrderBy
// (ties by key). Both are DuckDB SQL over key, w0, w1, ... (each window's
// count) and t0, t1, ... (each window's total), written in code - never
// from a request. Limit 0 only counts the keys passing Where (Matched).
type GroupSelectionDTO struct {
	Where     string
	WhereArgs []any
	OrderBy   string
	OrderArgs []any
	Limit     int
}

type GroupWindowsResponseDTO struct {
	// Totals: the logs in each window.
	Totals []int64
	// Keys: the group keys counted.
	Keys       int64
	Selections []GroupSelectionResultDTO
}

type GroupSelectionResultDTO struct {
	Rows []GroupCountsDTO
	// Matched: the keys passing Where; more than len(Rows) when Limit cut them.
	Matched int64
}

// GroupCountsDTO is one key's count in each window.
type GroupCountsDTO struct {
	Key    string
	Counts []int64
}

// Truncated reports whether Limit left out keys passing Where.
func (r GroupSelectionResultDTO) Truncated() bool {
	return r.Matched > int64(len(r.Rows))
}
