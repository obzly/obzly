package inventory

import (
	"time"

	"github.com/google/uuid"
)

// AssetDTO is an asset with its project's name.
type AssetDTO struct {
	Asset
	ProjectName string `json:"projectName"`
	// ChildCount is how many assets have this one as parent: only with childCounts=true.
	ChildCount *int64 `json:"childCount,omitempty"`
}

type AssetListDTO struct {
	Assets []AssetDTO `json:"assets"`
	// Total is how many matched; Assets holds the page of them from offset, at most limit.
	Total int64 `json:"total"`
	// Ancestors: only with ancestors=true. The parents, up to the top, of Assets that are not in
	// Assets themselves, so a tree can show where each match sits.
	Ancestors []AssetDTO `json:"ancestors,omitempty"`
}

// AssetRefDTO points at a related asset.
type AssetRefDTO struct {
	ID      uuid.UUID `json:"id"`
	Kind    string    `json:"kind"`
	Subtype string    `json:"subtype"`
	Name    string    `json:"name"`
	Health  string    `json:"health"`
}

type AssetDetailDTO struct {
	Asset AssetDTO `json:"asset"`
	// Parents run from the top (cluster) down to the direct parent.
	Parents []AssetRefDTO `json:"parents"`
	// Node is the node a pod or container runs on.
	Node     *AssetRefDTO `json:"node,omitempty"`
	Children []AssetDTO   `json:"children"`
	Changes  []Change     `json:"changes"`
}

type ChangeDTO struct {
	Change
	ProjectName string `json:"projectName"`
	// AssetID is the asset's id while it is still in the inventory.
	AssetID *uuid.UUID `json:"assetId,omitempty"`
}

type ProjectCountDTO struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Assets int64     `json:"assets"`
	// Live counts the project's assets that are not terminated, by health.
	Live map[string]int64 `json:"live"`
}

// RegionCountDTO counts the live assets (pods and containers aside) reporting a cloud region.
type RegionCountDTO struct {
	// Provider is cloud_provider, "" when not reported.
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Assets   int64  `json:"assets"`
	Warning  int64  `json:"warning"`
	Critical int64  `json:"critical"`
	// Projects are the names of the projects with assets there.
	Projects []string `json:"projects"`
}

type SummaryDTO struct {
	Total int64 `json:"total"`
	// Kinds counts assets by kind, then health.
	Kinds    map[string]map[string]int64 `json:"kinds"`
	Health   map[string]int64            `json:"health"`
	States   map[string]int64            `json:"states"`
	Projects []ProjectCountDTO           `json:"projects"`
	// Live counts the assets that are not terminated, by health.
	Live map[string]int64 `json:"live"`
	// Regions: most assets first.
	Regions []RegionCountDTO `json:"regions"`
	// Clusters are the names of the cluster assets, sorted.
	Clusters []string `json:"clusters"`
	// History: only with history=true.
	History *HistoryDTO `json:"history,omitempty"`
}

// HistoryDTO is the inventory's hourly counts, as the builder left them at the end of each hour.
type HistoryDTO struct {
	// Hours with counts over the last 7 days, oldest first.
	Hours []time.Time `json:"hours"`
	// Kinds: each kind's assets (every health) at each of Hours.
	Kinds map[string][]int64 `json:"kinds"`
	// PreviousAt is the newest hour at least 24 h old (absent when none is from the 2 hours
	// before that); Previous counts its assets by kind, then health.
	PreviousAt *time.Time                  `json:"previousAt,omitempty"`
	Previous   map[string]map[string]int64 `json:"previous,omitempty"`
}
