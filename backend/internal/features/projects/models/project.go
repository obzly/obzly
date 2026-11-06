package projects_models

import (
	"obzly/internal/features/orgplan"
	users_models "obzly/internal/features/users/models"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Project limits and quotas of 0 mean unlimited (no plan, or a plan without limits).
type Project struct {
	ID        uuid.UUID `json:"id"        gorm:"column:id"`
	Name      string    `json:"name"      gorm:"column:name"`
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at"`

	// Plan
	PlanID *uuid.UUID             `json:"planId" gorm:"column:plan_id"`
	Plan   *users_models.UserPlan `json:"plan"   gorm:"foreignKey:PlanID"`
	// OrgPlan is the organization plan this Site enforces, when it limits anything
	// (for the settings page; the Effective* methods read orgplan.Current).
	OrgPlan *orgplan.Limits `json:"orgPlan,omitempty" gorm:"-"`

	// Security Policies
	IsApiKeyRequired  bool     `json:"isApiKeyRequired" gorm:"column:is_api_key_required"`
	IsFilterByDomain  bool     `json:"isFilterByDomain" gorm:"column:is_filter_by_domain"`
	IsFilterByIP      bool     `json:"isFilterByIp"     gorm:"column:is_filter_by_ip"`
	AllowedDomainsRaw string   `json:"-"                gorm:"column:allowed_domains_raw"`
	AllowedDomains    []string `json:"allowedDomains"   gorm:"-"`
	AllowedIPsRaw     string   `json:"-"                gorm:"column:allowed_ips_raw"`
	AllowedIPs        []string `json:"allowedIps"       gorm:"-"`

	// IsRateLimitingEnabled gates LogsPerSecondLimit/MaxLogsAmount/MaxLogsSizeMB/
	// MaxLogSizeKB only, so a high-volume producer can avoid any capacity-driven drops.
	// Retention (MaxLogsLifeDays) is a lifecycle policy and applies regardless.
	IsRateLimitingEnabled bool  `json:"isRateLimitingEnabled" gorm:"column:is_rate_limiting_enabled"`
	LogsPerSecondLimit    int   `json:"logsPerSecondLimit"    gorm:"column:logs_per_second_limit"`
	MaxLogsAmount         int64 `json:"maxLogsAmount"         gorm:"column:max_logs_amount"`
	MaxLogsSizeMB         int   `json:"maxLogsSizeMb"         gorm:"column:max_logs_size_mb"`
	MaxLogsLifeDays       int   `json:"maxLogsLifeDays"       gorm:"column:max_logs_life_days"`
	MaxLogSizeKB          int   `json:"maxLogSizeKb"          gorm:"column:max_log_size_kb"`

	// IsSystem marks the reserved project holding Obzly's own request traces
	// (EnsureSystemProject). It has no members and cannot be deleted.
	IsSystem bool `json:"isSystem" gorm:"column:is_system"`

	// Cache-related fields for logs insertion
	IsNotExists bool `json:"isNotExists,omitempty" gorm:"-"` // Used for caching non-existent projects
}

func (Project) TableName() string {
	return "projects"
}

func (p *Project) BeforeSave(tx *gorm.DB) error {
	if len(p.AllowedDomains) > 0 {
		p.AllowedDomainsRaw = strings.Join(p.AllowedDomains, ",")
	} else {
		p.AllowedDomainsRaw = ""
	}

	if len(p.AllowedIPs) > 0 {
		p.AllowedIPsRaw = strings.Join(p.AllowedIPs, ",")
	} else {
		p.AllowedIPsRaw = ""
	}

	return nil
}

func (p *Project) AfterFind(tx *gorm.DB) error {
	if p.AllowedDomainsRaw != "" {
		p.AllowedDomains = strings.Split(p.AllowedDomainsRaw, ",")
		for i, domain := range p.AllowedDomains {
			p.AllowedDomains[i] = strings.TrimSpace(domain)
		}
	} else {
		p.AllowedDomains = []string{}
	}

	if p.AllowedIPsRaw != "" {
		p.AllowedIPs = strings.Split(p.AllowedIPsRaw, ",")
		for i, ip := range p.AllowedIPs {
			p.AllowedIPs[i] = strings.TrimSpace(ip)
		}
	} else {
		p.AllowedIPs = []string{}
	}

	return nil
}

func (p *Project) UpdateFromDTO(updateDTO *Project) {
	p.Name = updateDTO.Name
	p.IsApiKeyRequired = updateDTO.IsApiKeyRequired
	p.IsFilterByDomain = updateDTO.IsFilterByDomain
	p.IsFilterByIP = updateDTO.IsFilterByIP
	p.AllowedDomains = updateDTO.AllowedDomains
	p.AllowedIPs = updateDTO.AllowedIPs
	// Not plan-gated: plans set limit values, never whether enforcement is on.
	p.IsRateLimitingEnabled = updateDTO.IsRateLimitingEnabled

	if p.Plan == nil || p.Plan.LogsPerSecondLimit == 0 {
		p.LogsPerSecondLimit = updateDTO.LogsPerSecondLimit
	}

	if p.Plan == nil || p.Plan.MaxLogsAmount == 0 {
		p.MaxLogsAmount = updateDTO.MaxLogsAmount
	}

	if p.Plan == nil || p.Plan.MaxLogsSizeMB == 0 {
		p.MaxLogsSizeMB = updateDTO.MaxLogsSizeMB
	}

	if p.Plan == nil || p.Plan.MaxLogsLifeDays == 0 {
		p.MaxLogsLifeDays = updateDTO.MaxLogsLifeDays
	}

	if p.Plan == nil || p.Plan.MaxLogSizeKB == 0 {
		p.MaxLogSizeKB = updateDTO.MaxLogSizeKB
	}
}

// EffectiveLogsPerSecondLimit is the project's rate limit when it has rate
// limiting on, capped by its organization's plan, which applies either
// way; 0 is unlimited. EffectiveMaxLogSizeKB likewise.
func (p *Project) EffectiveLogsPerSecondLimit() int {
	own := 0
	if p.IsRateLimitingEnabled {
		own = p.LogsPerSecondLimit
	}
	return orgplan.Stricter(own, orgplan.Current().LogsPerSecondLimit)
}

func (p *Project) EffectiveMaxLogSizeKB() int {
	own := 0
	if p.IsRateLimitingEnabled {
		own = p.MaxLogSizeKB
	}
	return orgplan.Stricter(own, orgplan.Current().MaxLogSizeKB)
}

// EffectiveMaxLogsAmount and EffectiveMaxLogsSizeMB are the cleanup
// quotas: the project's with rate limiting on (a streaming producer may
// turn them off), capped by its organization's plan either way.
func (p *Project) EffectiveMaxLogsAmount() int64 {
	var own int64
	if p.IsRateLimitingEnabled {
		own = p.MaxLogsAmount
	}
	return orgplan.Stricter64(own, orgplan.Current().MaxLogsAmount)
}

func (p *Project) EffectiveMaxLogsSizeMB() int {
	own := 0
	if p.IsRateLimitingEnabled {
		own = p.MaxLogsSizeMB
	}
	return orgplan.Stricter(own, orgplan.Current().MaxLogsSizeMB)
}

func (p *Project) SetLimitsFromPlan(plan *users_models.UserPlan) {
	if plan == nil {
		return
	}

	if plan.LogsPerSecondLimit > 0 {
		p.LogsPerSecondLimit = plan.LogsPerSecondLimit
	}

	if plan.MaxLogsAmount > 0 {
		p.MaxLogsAmount = plan.MaxLogsAmount
	}

	if plan.MaxLogsSizeMB > 0 {
		p.MaxLogsSizeMB = plan.MaxLogsSizeMB
	}

	if plan.MaxLogsLifeDays > 0 {
		p.MaxLogsLifeDays = plan.MaxLogsLifeDays
	}

	if plan.MaxLogSizeKB > 0 {
		p.MaxLogSizeKB = plan.MaxLogSizeKB
	}
}
