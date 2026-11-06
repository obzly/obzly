package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ValidateTLSSettings_CertWithoutKey_ReturnsError(t *testing.T) {
	cases := map[string]EnvVariables{
		"OTLP_GRPC_TLS_KEY_FILE":      {OtlpGrpcTLSCertFile: "/certs/tls.crt"},
		"OTLP_GRPC_TLS_CERT_FILE":     {OtlpGrpcTLSKeyFile: "/certs/tls.key"},
		"QUERY_RESULTS_TLS_KEY_FILE":  {QueryResultsTLSCertFile: "/certs/tls.crt"},
		"QUERY_RESULTS_TLS_CERT_FILE": {QueryResultsTLSKeyFile: "/certs/tls.key"},
	}
	for missing, env := range cases {
		err := ValidateTLSSettings(env)
		require.Error(t, err, missing)
		assert.Contains(t, err.Error(), missing)
	}
}

func Test_ValidateTLSSettings_PairsSetOrUnset_ReturnsNil(t *testing.T) {
	require.NoError(t, ValidateTLSSettings(EnvVariables{}))
	require.NoError(t, ValidateTLSSettings(EnvVariables{
		OtlpGrpcTLSCertFile: "/certs/tls.crt", OtlpGrpcTLSKeyFile: "/certs/tls.key",
		QueryResultsTLSCertFile: "/certs/tls.crt", QueryResultsTLSKeyFile: "/certs/tls.key",
	}))
	// A coordinator needs only the CA to dial TLS.
	require.NoError(t, ValidateTLSSettings(EnvVariables{QueryResultsTLSCAFile: "/certs/ca.crt"}))
}

func Test_QueryResultsClientTLS_OnWithAnyResultsTLSSetting(t *testing.T) {
	assert.False(t, EnvVariables{}.QueryResultsClientTLS())
	assert.True(t, EnvVariables{QueryResultsTLSCAFile: "/certs/ca.crt"}.QueryResultsClientTLS())
	assert.True(t, EnvVariables{QueryResultsTLSServerName: "obzly-querier.example.svc"}.QueryResultsClientTLS())
	assert.True(t, EnvVariables{
		QueryResultsTLSCertFile: "/certs/tls.crt", QueryResultsTLSKeyFile: "/certs/tls.key",
	}.QueryResultsClientTLS())
}
