// Package system_siteinfo serves GET /api/v1/site/info: an unauthenticated, secret-free
// description of this backend. A control plane calls it when registering a Site and
// compares SiteID, catching an api_url that points at the wrong backend. It publishes
// no keys; only the control plane does.
package system_siteinfo

// SiteInfoResponse is the wire shape. Mode is "standalone" (no control
// plane configured), "site" (token exchange enabled), or "control-plane".
type SiteInfoResponse struct {
	SiteID                 string `json:"siteId"`
	SiteName               string `json:"siteName"`
	Version                string `json:"version"`
	Mode                   string `json:"mode"`
	ControlPlaneConfigured bool   `json:"controlPlaneConfigured"`
	// SharedConfigKey is what this Site's control plane seals its
	// notification channels and email settings to (public).
	SharedConfigKey string `json:"sharedConfigKey,omitempty"`
}
