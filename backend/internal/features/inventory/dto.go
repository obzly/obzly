package inventory

import (
	"time"

	"github.com/google/uuid"
)

// AssetDTO is an asset with its project's name.
type AssetDTO struct {
	Asset
	ProjectName string `json:"projectName"`
}

type AssetListDTO struct {
	Assets []AssetDTO `json:"assets"`
	// Total is how many matched; Assets holds at most the limit.
	Total int64 `json:"total"`
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
}

type ProjectCountDTO struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Assets int64     `json:"assets"`
}

type SummaryDTO struct {
	Total int64 `json:"total"`
	// Kinds counts assets by kind, then health.
	Kinds    map[string]map[string]int64 `json:"kinds"`
	Health   map[string]int64            `json:"health"`
	States   map[string]int64            `json:"states"`
	Projects []ProjectCountDTO           `json:"projects"`
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
