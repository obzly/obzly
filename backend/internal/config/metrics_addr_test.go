package config

import "testing"

func Test_ValidateMetricsAddr_AcceptsDefaultOffAndHostPort(t *testing.T) {
	for _, addr := range []string{DefaultMetricsAddr, MetricsAddrOff, "0.0.0.0:9464", "127.0.0.1:9100"} {
		if err := ValidateMetricsAddr(addr, DefaultHTTPPort, DefaultOtlpGrpcPort); err != nil {
			t.Errorf("ValidateMetricsAddr(%q) = %v, want nil", addr, err)
		}
	}
}

func Test_ValidateMetricsAddr_WhenInvalid_ReturnsError(t *testing.T) {
	cases := []string{
		"9464",      // no colon
		":0",        // port out of range
		":70000",    // port out of range
		":abc",      // not a number
		":4005",     // clashes with HTTP_PORT
		"host:4317", // clashes with OTLP_GRPC_PORT
		"disabled",  // only "off" disables
	}
	for _, addr := range cases {
		if err := ValidateMetricsAddr(addr, DefaultHTTPPort, DefaultOtlpGrpcPort); err == nil {
			t.Errorf("ValidateMetricsAddr(%q) = nil, want error", addr)
		}
	}
}

func Test_MetricsEnabled_WhenOff_ReturnsFalse(t *testing.T) {
	if (EnvVariables{MetricsAddr: MetricsAddrOff}).MetricsEnabled() {
		t.Error("MetricsEnabled() = true for \"off\"")
	}
	if !(EnvVariables{MetricsAddr: DefaultMetricsAddr}).MetricsEnabled() {
		t.Error("MetricsEnabled() = false for the default address")
	}
}

func Test_ParseSelfTracingSampleRate_DefaultsAndBounds(t *testing.T) {
	valid := map[string]float64{"": DefaultSelfTracingSampleRate, " ": DefaultSelfTracingSampleRate, "0": 0, "0.25": 0.25, "1": 1}
	for raw, want := range valid {
		got, err := ParseSelfTracingSampleRate(raw)
		if err != nil || got != want {
			t.Errorf("ParseSelfTracingSampleRate(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-0.1", "1.5", "one percent"} {
		if _, err := ParseSelfTracingSampleRate(raw); err == nil {
			t.Errorf("ParseSelfTracingSampleRate(%q) = nil error, want error", raw)
		}
	}
}
