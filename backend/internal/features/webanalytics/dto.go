package webanalytics

import "time"

// ReportDTO is a Web analytics report for one range: closed hours from
// rollups merged with the live part read from raw logs.
type ReportDTO struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// BucketSeconds is the width of a Series bucket.
	BucketSeconds int64          `json:"bucketSeconds"`
	Rollups       RollupInfoDTO  `json:"rollups"`
	Live          LiveInfoDTO    `json:"live"`
	Sources       []SourceDTO    `json:"sources"`
	Selected      []string       `json:"selected"`
	Hosts         []HostDTO      `json:"hosts"`
	Totals        TotalsDTO      `json:"totals"`
	Previous      *TotalsDTO     `json:"previous"`
	Series        []BucketDTO    `json:"series"`
	Heatmap       []HourCountDTO `json:"heatmap"`
	Panels        PanelsDTO      `json:"panels"`
	Geo           GeoDTO         `json:"geo"`
	IPMode        string         `json:"ipMode"`
	ExcludeBots   bool           `json:"excludeBots"`
}

// RollupInfoDTO says which part of the range came from stored rollups.
type RollupInfoDTO struct {
	// Used is false for ranges under 2 hours, read entirely from raw logs.
	Used  bool       `json:"used"`
	Hours int        `json:"hours"`
	From  *time.Time `json:"from"`
	To    *time.Time `json:"to"`
	// LastBuiltAt is the newest build among the hours used.
	LastBuiltAt *time.Time `json:"lastBuiltAt"`
	// Missing hours in the range are not summarised yet (the worker is
	// backfilling) and not in the report.
	Missing int `json:"missing"`
}

// LiveInfoDTO describes the part read from raw logs now.
type LiveInfoDTO struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Rows       int64     `json:"rows"`
	DurationMs int64     `json:"durationMs"`
	Cached     bool      `json:"cached"`
}

// SourceDTO is one access-log source (after merges) with its share of the
// range's requests.
type SourceDTO struct {
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	DefaultName  string            `json:"defaultName"`
	IdentifiedBy string            `json:"identifiedBy"`
	Identity     []AttributeDTO    `json:"identity"`
	Kind         string            `json:"kind"`
	Format       string            `json:"format"`
	Fields       map[string]string `json:"fields"`
	Where        string            `json:"where"`
	Hosts        []string          `json:"hosts"`
	Requests     int64             `json:"requests"`
	Share        float64           `json:"share"`
	HasReferrer  bool              `json:"hasReferrer"`
	HasUserAgent bool              `json:"hasUserAgent"`
	HasDuration  bool              `json:"hasDuration"`
	HasTLS       bool              `json:"hasTls"`
	// MergedFrom lists the keys of sources merged into this one.
	MergedFrom []string  `json:"mergedFrom"`
	MergedInto string    `json:"mergedInto,omitempty"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
}

type AttributeDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HostDTO struct {
	Host     string `json:"host"`
	Requests int64  `json:"requests"`
}

// TotalsDTO: Visitors is nil when no selected source logs client addresses;
// P95Ms is nil when none logs a duration.
type TotalsDTO struct {
	Requests        int64    `json:"requests"`
	Visitors        *int64   `json:"visitors"`
	Failed          int64    `json:"failed"`
	NotFound        int64    `json:"notFound"`
	Bytes           int64    `json:"bytes"`
	BotRequests     int64    `json:"botRequests"`
	P95Ms           *float64 `json:"p95Ms"`
	DurationSources []string `json:"durationSources"`
}

type BucketDTO struct {
	Start    time.Time `json:"start"`
	Live     bool      `json:"live"`
	Requests int64     `json:"requests"`
	Status2  int64     `json:"status2xx"`
	Status3  int64     `json:"status3xx"`
	Status4  int64     `json:"status4xx"`
	Status5  int64     `json:"status5xx"`
	Other    int64     `json:"other"`
	Bytes    int64     `json:"bytes"`
	Visitors int64     `json:"visitors"`
}

type HourCountDTO struct {
	Hour     time.Time `json:"hour"`
	Requests int64     `json:"requests"`
}

// RowDTO is one top-list row. Visitors is nil for lists without them.
type RowDTO struct {
	Key       string     `json:"key"`
	Hits      int64      `json:"hits"`
	Visitors  *int64     `json:"visitors,omitempty"`
	Bytes     int64      `json:"bytes"`
	Errors5xx int64      `json:"errors5xx"`
	LastSeen  *time.Time `json:"lastSeen,omitempty"`
	Tag       string     `json:"tag,omitempty"`
	Country   string     `json:"country,omitempty"`
}

type PanelsDTO struct {
	Pages     []RowDTO `json:"pages"`
	NotFound  []RowDTO `json:"notFound"`
	Static    []RowDTO `json:"static"`
	Referrers []RowDTO `json:"referrers"`
	IPs       []RowDTO `json:"ips"`
	Browsers  []RowDTO `json:"browsers"`
	Systems   []RowDTO `json:"systems"`
	Devices   []RowDTO `json:"devices"`
	Countries []RowDTO `json:"countries"`
	VHosts    []RowDTO `json:"vhosts"`
	Methods   []RowDTO `json:"methods"`
	Protocols []RowDTO `json:"protocols"`
	TLS       []RowDTO `json:"tls"`
	Status    []RowDTO `json:"status"`
}

// GeoDTO: country lookups and the attribution DB-IP's licence requires.
type GeoDTO struct {
	Enabled      bool   `json:"enabled"`
	Available    bool   `json:"available"`
	DatabaseType string `json:"databaseType,omitempty"`
	Attribution  string `json:"attribution"`
	Error        string `json:"error,omitempty"`
}

// SettingsDTO is the settings drawer: the editable settings plus how the
// rollups are doing.
type SettingsDTO struct {
	IPMode           string           `json:"ipMode"`
	RetentionDays    int              `json:"retentionDays"`
	LogRetentionDays int              `json:"logRetentionDays"`
	ExcludeBots      bool             `json:"excludeBots"`
	StaticExtensions string           `json:"staticExtensions"`
	GeoEnabled       bool             `json:"geoEnabled"`
	Geo              GeoDTO           `json:"geo"`
	Sources          []SourceDTO      `json:"sources"`
	Build            *BuildDTO        `json:"build"`
	Storage          StorageDTO       `json:"storage"`
	Schema           []SchemaFieldDTO `json:"schema"`
	CanManage        bool             `json:"canManage"`
	BuildDelayMin    int              `json:"buildDelayMinutes"`
}

type BuildDTO struct {
	Hour       time.Time `json:"hour"`
	BuiltAt    time.Time `json:"builtAt"`
	DurationMs int64     `json:"durationMs"`
	RowsRead   int64     `json:"rowsRead"`
}

type StorageDTO struct {
	Hours int64 `json:"hours"`
	Bytes int64 `json:"bytes"`
}

// SchemaFieldDTO is one normalized field and the names it's read from.
type SchemaFieldDTO struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Aliases []string `json:"aliases"`
}

// UpdateSettingsDTO: nil fields are left as they are.
type UpdateSettingsDTO struct {
	IPMode           *string           `json:"ipMode"`
	RetentionDays    *int              `json:"retentionDays"`
	ExcludeBots      *bool             `json:"excludeBots"`
	StaticExtensions *string           `json:"staticExtensions"`
	GeoEnabled       *bool             `json:"geoEnabled"`
	Sources          []UpdateSourceDTO `json:"sources"`
}

// UpdateSourceDTO renames a source ("" restores its default name) or merges
// it into another ("" un-merges).
type UpdateSourceDTO struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	MergedInto string `json:"mergedInto"`
}
