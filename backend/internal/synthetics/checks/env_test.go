package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
)

func Test_EnvPolicy_SecretPrefixAndAllowList_Resolve_CloudAndProbeVariablesNever(t *testing.T) {
	environment := map[string]string{
		"OBZLY_SECRET_API_KEY": "k1", "PAYMENTS_TOKEN": "k2", "TEAM_A_KEY": "k3", "OTHER": "k4",
		"AWS_SECRET_ACCESS_KEY": "aws", "OBZLY_PROBE_TOKEN": "obz_prb_x", "AZURE_CLIENT_SECRET": "az",
		"GOOGLE_APPLICATION_CREDENTIALS": "g",
	}
	policy := NewEnvPolicy("PAYMENTS_TOKEN, TEAM_A_*, AWS_*, OBZLY_PROBE_TOKEN", func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	})

	for _, name := range []string{"OBZLY_SECRET_API_KEY", "PAYMENTS_TOKEN", "TEAM_A_KEY"} {
		value, ok := policy.Lookup(name)
		require.True(t, ok, name)
		require.Equal(t, environment[name], value)
	}
	for _, name := range []string{
		"OTHER", "AWS_SECRET_ACCESS_KEY", "OBZLY_PROBE_TOKEN", "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION_CREDENTIALS",
	} {
		_, ok := policy.Lookup(name)
		require.False(t, ok, name)
	}
}

func Test_Run_RefusedEnvPlaceholder_FailsNamingIt_WithoutItsValue(t *testing.T) {
	check := httpCheck("https://api.example.com/")
	check.Definition.Request.Headers = map[string]string{"X-Key": "${env:AWS_SECRET_ACCESS_KEY}"}
	runner := testRunner()
	runner.Env = NewEnvPolicy("AWS_SECRET_ACCESS_KEY", func(string) (string, bool) { return "wJalrXUtnFEMI", true })

	result := runner.Run(context.Background(), check)

	require.Equal(t, spec.StatusDown, result.Status)
	require.Contains(t, result.Error, "${env:AWS_SECRET_ACCESS_KEY}")
	require.Contains(t, result.Error, "OBZLY_PROBE_ENV_ALLOW")
	require.NotContains(t, result.Error, "wJalrXUtnFEMI")
}

func Test_Run_SecretValues_StayOutOfTheLabelAndTheResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("X-Key"))
		_, _ = w.Write([]byte("key=" + r.URL.Query().Get("key")))
	}))
	defer server.Close()
	check := httpCheck(server.URL+"/?key=${env:OBZLY_SECRET_QUERY}", spec.Assertion{
		Kind: spec.AssertBody, Op: spec.OpContains, Value: "nothing-like-this", Severity: spec.SeverityFail,
	})
	check.Definition.Request.Headers = map[string]string{"X-Key": "control-plane-secret"}
	check.Redact = []string{"control-plane-secret"}
	runner := testRunner()
	runner.Env = NewEnvPolicy("", func(string) (string, bool) { return "probe-env-secret", true })

	result := runner.Run(context.Background(), check)

	require.Equal(t, server.URL+"/?key=${env:OBZLY_SECRET_QUERY}", result.Target)
	require.Equal(t, "key=[redacted]", result.BodySnippet)
	require.Equal(t, "[redacted]", result.Headers["X-Echo"])
	for _, assertion := range result.Assertions {
		require.NotContains(t, assertion.Actual, "probe-env-secret")
	}
}

func Test_ProbePolicy_RefusesCloudMetadata_AndPrivateIPv4CarriedInIPv6(t *testing.T) {
	policy := ProbePolicy([]*net.IPNet{mustCIDR("10.20.0.0/16")})
	for _, address := range []string{
		"fd00:ec2::254", "168.63.129.16", "100.100.100.200", "192.0.0.192",
		"64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "::169.254.169.254", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "64:ff9b::a14:1", "10.20.3.4",
	} {
		require.Error(t, policy(net.ParseIP(address)), address)
	}
	for _, address := range []string{"10.0.0.5", "192.168.1.10", "fd12::1", "64:ff9b::a00:5", "1.1.1.1"} {
		require.NoError(t, policy(net.ParseIP(address)), address, "private ranges stay allowed on a probe")
	}
}

func Test_RunDNS_ResolverAddress_GoesThroughThePolicy(t *testing.T) {
	runner := &Runner{Location: "test", Probe: "p", Policy: ProbePolicy(nil)}

	result := runner.Run(context.Background(), spec.ProbeCheck{Definition: spec.Definition{
		Type: spec.TypeDNS, Target: "example.com", TimeoutSeconds: 2,
		DNS: &spec.DNSOptions{RecordType: "A", Resolver: "169.254.169.253:53"},
	}})

	require.Equal(t, spec.StatusDown, result.Status)
	require.Contains(t, result.Error, "not allowed")
}
