package control_plane_services

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// CheckHTTPClient backs SiteController.CheckSite. Hardened like exchangeHTTPClient (no
// redirects, pinned dialer): the target is admin-typed and its response is untrusted.
var CheckHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("redirects are not followed for a site check")
	},
	Transport: newHardenedTransport(),
}

// siteInfoMaxBytes bounds how much of a site-info response is read.
const siteInfoMaxBytes = 16 * 1024

// SiteInfo is the subset of GET /api/v1/site/info the control plane uses. Duplicated
// from the Site side on purpose; only the wire JSON has to agree.
type SiteInfo struct {
	SiteID  string `json:"siteId"`
	Version string `json:"version"`
	// SharedConfigKey is the Site's public key that shared channel/SMTP settings are
	// sealed to.
	SharedConfigKey string `json:"sharedConfigKey"`
}

func DecodeSiteInfo(body io.Reader) (SiteInfo, error) {
	limited := io.LimitReader(body, siteInfoMaxBytes)
	var info SiteInfo
	if err := json.NewDecoder(limited).Decode(&info); err != nil {
		return SiteInfo{}, err
	}
	return info, nil
}
