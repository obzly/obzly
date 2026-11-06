package config

import (
	"testing"
	"time"
)

var allServiceModes = []ServiceMode{
	ServiceModeAll,
	ServiceModeAPI,
	ServiceModeWorker,
	ServiceModeControlPlane,
	ServiceModeBroker,
	ServiceModeQuerier,
}

func Test_ServiceMode_ServesHTTP(t *testing.T) {
	cases := []struct {
		mode ServiceMode
		want bool
	}{
		{ServiceModeAll, true},
		{ServiceModeAPI, true},
		{ServiceModeWorker, false},
		{ServiceModeControlPlane, true},
	}
	for _, tc := range cases {
		if got := tc.mode.ServesHTTP(); got != tc.want {
			t.Errorf("%s.ServesHTTP() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func Test_ServiceMode_RunsBackgroundWorkers(t *testing.T) {
	cases := []struct {
		mode ServiceMode
		want bool
	}{
		{ServiceModeAll, true},
		{ServiceModeAPI, false},
		{ServiceModeWorker, true},
		{ServiceModeControlPlane, false},
	}
	for _, tc := range cases {
		if got := tc.mode.RunsBackgroundWorkers(); got != tc.want {
			t.Errorf("%s.RunsBackgroundWorkers() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// The control plane must be the only mode without signal storage; every other mode must
// answer true, or main.go stops wiring storage for replicas that hold data.
func Test_ServiceMode_RunsSignalStorage(t *testing.T) {
	cases := []struct {
		mode ServiceMode
		want bool
	}{
		{ServiceModeAll, true},
		{ServiceModeAPI, true},
		{ServiceModeWorker, true},
		{ServiceModeControlPlane, false},
	}
	for _, tc := range cases {
		if got := tc.mode.RunsSignalStorage(); got != tc.want {
			t.Errorf("%s.RunsSignalStorage() = %v, want %v", tc.mode, got, tc.want)
		}
	}
	if !ServiceModeControlPlane.IsControlPlane() || ServiceModeAll.IsControlPlane() {
		t.Error("IsControlPlane() must be true for exactly ServiceModeControlPlane")
	}
}

// Every mode must serve HTTP or run workers, or it would start a process that does
// nothing.
func Test_ServiceMode_EveryValidModeDoesSomething(t *testing.T) {
	for _, mode := range allServiceModes {
		if !mode.ServesHTTP() && !mode.RunsBackgroundWorkers() && !mode.IsBroker() && !mode.IsQuerier() {
			t.Errorf("%s does neither ServesHTTP() nor RunsBackgroundWorkers() - main.go would start nothing", mode)
		}
	}
}

func Test_ParseCIDRList(t *testing.T) {
	nets, err := ParseCIDRList(" 10.0.0.0/8, 192.168.1.5 ,, 2001:db8::/32 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("expected 3 networks, got %d", len(nets))
	}
	if ones, bits := nets[1].Mask.Size(); ones != 32 || bits != 32 {
		t.Errorf("a bare IPv4 should become a /32, got /%d", ones)
	}
	if _, err := ParseCIDRList("not-an-ip"); err == nil {
		t.Error("expected an error for a garbage entry")
	}
	if nets, err := ParseCIDRList(""); err != nil || nets != nil {
		t.Errorf("empty input should be (nil, nil), got (%v, %v)", nets, err)
	}
}

func Test_validateOriginURL(t *testing.T) {
	valid := []string{"https://obzly.example.com", "http://localhost:4005", "https://cp.internal:8443/"}
	for _, v := range valid {
		if err := validateOriginURL(v); err != nil {
			t.Errorf("%q should be a valid origin: %v", v, err)
		}
	}
	invalid := []string{
		"obzly.example.com",                 // no scheme
		"ftp://obzly.example.com",           // wrong scheme
		"https://obzly.example.com/api",     // path
		"https://obzly.example.com?x=1",     // query
		"https://obzly.example.com#frag",    // fragment
		"https://user:pw@obzly.example.com", // userinfo
	}
	for _, v := range invalid {
		if err := validateOriginURL(v); err == nil {
			t.Errorf("%q should be rejected as an origin", v)
		}
	}
}

func Test_SiteIDPattern(t *testing.T) {
	for _, ok := range []string{"a", "site-a", "eu-west-1", "0abc"} {
		if !SiteIDPattern.MatchString(ok) {
			t.Errorf("%q should be a valid site id", ok)
		}
	}
	for _, bad := range []string{"", "-a", "Site", "a_b", "a/b", "a b", "a." + "b"} {
		if SiteIDPattern.MatchString(bad) {
			t.Errorf("%q should be an invalid site id", bad)
		}
	}
}

// "admin" everywhere except a control plane ("superadmin", so it can't shadow a Site's
// root admin); ADMIN_EMAIL always wins.
func Test_EffectiveRootAdminEmail(t *testing.T) {
	cases := []struct {
		name string
		env  EnvVariables
		want string
	}{
		{"single-site default", EnvVariables{ServiceMode: ServiceModeAll}, "admin"},
		{"api replica default", EnvVariables{ServiceMode: ServiceModeAPI}, "admin"},
		{"worker default", EnvVariables{ServiceMode: ServiceModeWorker}, "admin"},
		{"control plane default", EnvVariables{ServiceMode: ServiceModeControlPlane}, "superadmin"},
		{
			"ADMIN_EMAIL wins on a site",
			EnvVariables{ServiceMode: ServiceModeAll, RootAdminEmail: "ops@example.com"},
			"ops@example.com",
		},
		{
			"ADMIN_EMAIL wins on a control plane",
			EnvVariables{ServiceMode: ServiceModeControlPlane, RootAdminEmail: "admin"},
			"admin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.EffectiveRootAdminEmail(); got != tc.want {
				t.Fatalf("EffectiveRootAdminEmail() = %q, want %q", got, tc.want)
			}
		})
	}
}

// HTTP_PORT / OTLP_GRPC_PORT must each be 1-65535 and differ.
func Test_ValidatePorts(t *testing.T) {
	cases := []struct {
		name      string
		http      string
		otlp      string
		wantError bool
	}{
		{"defaults", DefaultHTTPPort, DefaultOtlpGrpcPort, false},
		{"custom http port", "8080", DefaultOtlpGrpcPort, false},
		{"not a number", "80a", DefaultOtlpGrpcPort, true},
		{"zero", "0", DefaultOtlpGrpcPort, true},
		{"too large", "70000", DefaultOtlpGrpcPort, true},
		{"empty", "", DefaultOtlpGrpcPort, true},
		{"bad otlp port", DefaultHTTPPort, "-1", true},
		{"same port for both", "4317", "4317", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePorts(tc.http, tc.otlp)
			if tc.wantError && err == nil {
				t.Fatalf("ValidatePorts(%q, %q) = nil, want an error", tc.http, tc.otlp)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("ValidatePorts(%q, %q) = %v, want nil", tc.http, tc.otlp, err)
			}
		})
	}
	if DefaultHTTPPort != "4005" {
		t.Fatalf("DefaultHTTPPort = %q, want the historical 4005", DefaultHTTPPort)
	}
}

func Test_ServiceModeBroker_RunsOnlyTheBroker(t *testing.T) {
	mode := ServiceModeBroker
	if mode.ServesHTTP() || mode.RunsBackgroundWorkers() || mode.RunsSignalStorage() || !mode.IsBroker() {
		t.Error("broker must run only the NATS server")
	}
}

func Test_ServiceModeQuerier_RunsOnlyTheExecutor(t *testing.T) {
	mode := ServiceModeQuerier
	if mode.ServesHTTP() || mode.RunsBackgroundWorkers() || mode.RunsSignalStorage() || !mode.IsQuerier() {
		t.Error("querier must run only the query executor")
	}
}

func Test_ValidateQuerierSettings(t *testing.T) {
	complete := EnvVariables{
		ServiceMode: ServiceModeQuerier, NatsURL: "nats://broker:4222", NatsToken: "n",
		QuerierToken: "q", ObjectStorageURL: "s3://bucket",
	}
	without := func(change func(*EnvVariables)) EnvVariables {
		env := complete
		change(&env)
		return env
	}
	cases := []struct {
		name  string
		env   EnvVariables
		valid bool
	}{
		{"complete", complete, true},
		{"no NATS", without(func(e *EnvVariables) { e.NatsURL = "" }), false},
		{"no token", without(func(e *EnvVariables) { e.QuerierToken = "" }), false},
		{"no object storage", without(func(e *EnvVariables) { e.ObjectStorageURL = "" }), false},
		{"api needs none of it", EnvVariables{ServiceMode: ServiceModeAPI}, true},
	}
	for _, tc := range cases {
		if err := ValidateQuerierSettings(tc.env); (err == nil) != tc.valid {
			t.Errorf("%s: err = %v, want valid = %v", tc.name, err, tc.valid)
		}
	}
}

func Test_ValidateBrokerSettings(t *testing.T) {
	cases := []struct {
		name  string
		env   EnvVariables
		valid bool
	}{
		{
			"valkey default needs nothing",
			EnvVariables{ServiceMode: ServiceModeAPI, IngestBuffer: IngestBufferValkey},
			true,
		},
		{"unknown buffer", EnvVariables{ServiceMode: ServiceModeAll, IngestBuffer: "kafka"}, false},
		{"broker without token", EnvVariables{ServiceMode: ServiceModeBroker, IngestBuffer: IngestBufferValkey}, false},
		{"broker with token", EnvVariables{
			ServiceMode: ServiceModeBroker, IngestBuffer: IngestBufferValkey, NatsToken: "t",
		}, true},
		{"all embeds without URL or token", EnvVariables{
			ServiceMode: ServiceModeAll, IngestBuffer: IngestBufferJetStream,
		}, true},
		{"api needs a URL", EnvVariables{
			ServiceMode: ServiceModeAPI, IngestBuffer: IngestBufferJetStream, NatsToken: "t",
		}, false},
		{"worker needs a token", EnvVariables{
			ServiceMode: ServiceModeWorker, IngestBuffer: IngestBufferJetStream, NatsURL: "nats://broker:4222",
		}, false},
		{"api with URL and token", EnvVariables{
			ServiceMode: ServiceModeAPI, IngestBuffer: IngestBufferJetStream,
			NatsURL: "nats://broker:4222", NatsToken: "t",
		}, true},
		{"L0 max-age below the floor", EnvVariables{
			ServiceMode: ServiceModeAll, IngestBuffer: IngestBufferJetStream, IngestL0MaxAge: time.Minute,
		}, false},
		{"L0 max-age at the floor", EnvVariables{
			ServiceMode: ServiceModeAll, IngestBuffer: IngestBufferJetStream, IngestL0MaxAge: MinIngestL0MaxAge,
		}, true},
		{"control plane ignores jetstream", EnvVariables{
			ServiceMode: ServiceModeControlPlane, IngestBuffer: IngestBufferJetStream,
		}, true},
	}
	for _, tc := range cases {
		err := ValidateBrokerSettings(tc.env)
		if (err == nil) != tc.valid {
			t.Errorf("%s: ValidateBrokerSettings() = %v, want valid=%v", tc.name, err, tc.valid)
		}
	}
}

func Test_ApplyBrokerDefaults_ReplicasFollowTheTopology(t *testing.T) {
	embedded := EnvVariables{ServiceMode: ServiceModeAll, IngestBuffer: IngestBufferJetStream, LogsDataDir: "/data"}
	applyBrokerDefaults(&embedded)
	if embedded.JetStreamReplicas != 1 || embedded.JetStreamDir != "/data/jetstream" {
		t.Errorf("embedded broker: replicas=%d dir=%q, want 1 and /data/jetstream",
			embedded.JetStreamReplicas, embedded.JetStreamDir)
	}

	clustered := EnvVariables{
		ServiceMode: ServiceModeAPI, IngestBuffer: IngestBufferJetStream, NatsURL: "nats://broker:4222",
	}
	applyBrokerDefaults(&clustered)
	if clustered.JetStreamReplicas != 3 {
		t.Errorf("broker cluster: replicas=%d, want 3", clustered.JetStreamReplicas)
	}
}

func Test_ParseQueryMaxScanGB(t *testing.T) {
	cases := []struct {
		raw   string
		want  int64
		valid bool
	}{
		{"", DefaultQueryMaxScanGB << 30, true},
		{"0", 0, true},
		{" 250 ", 250 << 30, true},
		{"-1", 0, false},
		{"1.5", 0, false},
		{"10GB", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseQueryMaxScanGB(tc.raw)
		if (err == nil) != tc.valid || got != tc.want {
			t.Errorf("%q: got %d, %v; want %d, valid %v", tc.raw, got, err, tc.want, tc.valid)
		}
	}
}

func Test_ParseQuerierWarmHours(t *testing.T) {
	cases := []struct {
		raw   string
		want  int
		valid bool
	}{
		{"", DefaultQuerierWarmHours, true},
		{"0", 0, true},
		{" 168 ", 168, true},
		{"-1", 0, false},
		{"7d", 0, false},
		{"100000", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseQuerierWarmHours(tc.raw)
		if (err == nil) != tc.valid || got != tc.want {
			t.Errorf("%q: got %d, %v; want %d, valid %v", tc.raw, got, err, tc.want, tc.valid)
		}
	}
}
