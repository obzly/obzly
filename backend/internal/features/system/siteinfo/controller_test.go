package system_siteinfo

import (
	"testing"

	"obzly/internal/config"

	"github.com/stretchr/testify/assert"
)

// Pins the mode taxonomy a control plane relies on ("standalone", "site",
// "control-plane"), and that nothing secret is echoed.
func Test_describe(t *testing.T) {
	cases := []struct {
		name string
		env  config.EnvVariables
		want SiteInfoResponse
	}{
		{
			name: "standalone single-site install",
			env:  config.EnvVariables{ServiceMode: config.ServiceModeAll, AppVersion: "1.2.3"},
			want: SiteInfoResponse{Version: "1.2.3", Mode: "standalone"},
		},
		{
			name: "site enrolled in a control plane",
			env: config.EnvVariables{
				ServiceMode:        config.ServiceModeAPI,
				AppVersion:         "1.2.3",
				SiteID:             "eu-west-1",
				SiteName:           "EU West",
				ControlPlaneIssuer: "https://cp.example.com",
			},
			want: SiteInfoResponse{
				SiteID: "eu-west-1", SiteName: "EU West", Version: "1.2.3",
				Mode: "site", ControlPlaneConfigured: true,
			},
		},
		{
			name: "the control plane itself",
			env:  config.EnvVariables{ServiceMode: config.ServiceModeControlPlane, AppVersion: "1.2.3"},
			want: SiteInfoResponse{Version: "1.2.3", Mode: "control-plane"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describe(tc.env))
		})
	}
}
