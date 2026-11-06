package control_plane_services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_validateSiteURL(t *testing.T) {
	cases := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"valid https", "https://site-a.example.com", false, false},
		{"valid internal docker hostname", "http://site-a:4005", true, false},
		{"http rejected without allowInsecure", "http://site-a.example.com", false, true},
		{"http allowed with allowInsecure", "http://site-a.example.com", true, false},
		{"loopback literal rejected", "https://127.0.0.1", true, true},
		{"loopback literal rejected even insecure", "http://127.0.0.1", true, true},
		{"link-local rejected", "https://169.254.169.254", true, true},
		{"ipv6 loopback rejected", "https://[::1]", true, true},
		{"userinfo rejected", "https://user:pw@site-a.example.com", false, true},
		{"path rejected", "https://site-a.example.com/api", false, true},
		{"query rejected", "https://site-a.example.com?x=1", false, true},
		{"fragment rejected", "https://site-a.example.com#f", false, true},
		{"no host rejected", "https://", false, true},
		{"malformed url rejected", "://bad", false, true},
		{"private RFC1918 address allowed (docker/k8s internal)", "https://10.0.5.3", false, false},
		{"ULA address allowed", "https://[fd12:3456::5]", false, false},
		{"this-network rejected", "https://0.1.2.3", false, true},
		{"AWS IMDS over IPv6 rejected", "https://[fd00:ec2::254]", false, true},
		{"Azure WireServer rejected", "https://168.63.129.16", false, true},
		{"Alibaba metadata rejected", "https://100.100.100.200", false, true},
		{"metadata via NAT64 rejected", "https://[64:ff9b::a9fe:a9fe]", false, true},
		{"loopback via 6to4 rejected", "https://[2002:7f00:1::]", false, true},
		{"metadata via IPv4-compatible rejected", "https://[::a9fe:a9fe]", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSiteURL(tc.url, tc.allowInsecure)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_normalizeSiteDisplayName(t *testing.T) {
	name, err := normalizeSiteDisplayName("  US East  ")
	assert.NoError(t, err)
	assert.Equal(t, "US East", name)

	name, err = normalizeSiteDisplayName("")
	assert.NoError(t, err)
	assert.Equal(t, "", name)

	_, err = normalizeSiteDisplayName(strings.Repeat("é", maxSiteDisplayNameLength))
	assert.NoError(t, err, "the limit counts characters, not bytes")

	_, err = normalizeSiteDisplayName(strings.Repeat("a", maxSiteDisplayNameLength+1))
	assert.ErrorIs(t, err, ErrSiteInvalidDisplayName)
}

func Test_normalizeSiteEnvironment(t *testing.T) {
	for _, valid := range []string{"", "production", "staging", "development"} {
		environment, err := normalizeSiteEnvironment(valid)
		assert.NoError(t, err)
		assert.Equal(t, valid, environment)
	}

	environment, err := normalizeSiteEnvironment(" Production ")
	assert.NoError(t, err)
	assert.Equal(t, "production", environment)

	_, err = normalizeSiteEnvironment("prod")
	assert.ErrorIs(t, err, ErrSiteInvalidEnvironment)
}
