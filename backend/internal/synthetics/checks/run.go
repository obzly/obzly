// Package checks runs one synthetic check and describes the outcome as a
// spec.RunResult. obzly-probe runs it on a schedule; the control plane runs
// it for an editor Test when no probe can.
package checks

import (
	"context"
	"fmt"
	"net"
	"time"

	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

// Runner runs checks from one place.
type Runner struct {
	Location string
	Probe    string
	// Policy decides which addresses checks may connect to; nil is
	// ProbePolicy(nil).
	Policy egress.Policy
	// Env fills placeholders the control plane left from the probe's environment, where allowed;
	// nil leaves them and the run fails.
	Env *EnvPolicy
	// FromControlPlane marks results of control-plane Test runs.
	FromControlPlane bool
}

var (
	// Cloud metadata and host services outside 169.254/16: AWS IMDS over
	// IPv6, Azure WireServer, Alibaba Cloud metadata, Oracle's IMDS alias.
	metadataAddresses = []net.IP{
		net.ParseIP("fd00:ec2::254"), net.ParseIP("168.63.129.16"), net.ParseIP("100.100.100.200"),
		net.ParseIP("192.0.0.192"),
	}
	nat64Network     = mustCIDR("64:ff9b::/96")
	sixToFourNetwork = mustCIDR("2002::/16")
	compatNetwork    = mustCIDR("::/96")
)

// ProbePolicy refuses loopback, link-local (cloud metadata at 169.254/16), unspecified, multicast
// and other cloud metadata addresses, and deny, also inside NAT64, 6to4 or IPv4-compatible
// addresses. Private ranges are allowed: probes test internal services.
func ProbePolicy(deny []*net.IPNet) egress.Policy {
	return func(ip net.IP) error {
		if err := probeRefuses(ip, deny); err != nil {
			return err
		}
		if embedded := embeddedIPv4(ip); embedded != nil {
			if err := probeRefuses(embedded, deny); err != nil {
				return fmt.Errorf("address %s carries %s: %w", ip, embedded, err)
			}
		}
		return nil
	}
}

// Run runs check once. It never returns an error: a failure is a red
// result that says why.
func (r *Runner) Run(ctx context.Context, check spec.ProbeCheck) (result spec.RunResult) {
	definition, secrets, refused := r.resolve(check)
	// The control plane's label (from before it filled secrets), else the unfilled definition's,
	// so no secret value reaches a metric label.
	label := check.Target
	if label == "" {
		label = check.Definition.TargetLabel()
	}
	result = spec.RunResult{
		CheckID: check.ID, Check: check.Name, Type: definition.Type, Target: label,
		Location: r.Location, Probe: r.Probe, StartedAt: time.Now().UTC(), FromControlPlane: r.FromControlPlane,
	}
	defer func() { redact(&result, secrets) }()
	if missing := definition.Placeholders(); len(missing) > 0 {
		result.Status = spec.StatusDown
		result.Error = fmt.Sprintf(
			"secret %s has no value (set it on the monitor, or as an environment variable on the probe)",
			missing[0],
		)
		if len(refused) > 0 {
			result.Error = fmt.Sprintf("${env:%s} can't be read from the probe's environment: only %s* names "+
				"and those in OBZLY_PROBE_ENV_ALLOW are (never OBZLY_PROBE_*, AWS_*, AZURE_* or GOOGLE_*)",
				refused[0], SecretEnvPrefix)
		}
		return result
	}

	timeout := time.Duration(definition.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = spec.DefaultTimeoutSeconds * time.Second
		if definition.Type == spec.TypeMulti {
			timeout = spec.DefaultMultiTimeoutSeconds * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	policy := r.Policy
	if policy == nil {
		policy = ProbePolicy(nil)
	}
	switch definition.Type {
	case spec.TypeHTTP:
		runHTTP(ctx, policy, definition, &result)
	case spec.TypeTLS:
		runTLS(ctx, policy, definition, &result)
	case spec.TypeTCP:
		runTCP(ctx, policy, definition, &result)
	case spec.TypeDNS:
		runDNS(ctx, policy, definition, &result)
	case spec.TypeICMP:
		runICMP(ctx, policy, definition, &result)
	case spec.TypeMulti:
		runMulti(ctx, policy, definition, &result, func(secret string) { secrets = append(secrets, secret) })
		return result
	default:
		result.Error = fmt.Sprintf("unknown check type %q", definition.Type)
	}
	if ctx.Err() == context.DeadlineExceeded && result.Error == "" {
		result.Error = fmt.Sprintf("timed out after %s", timeout)
	}
	finish(definition, &result)
	return result
}

// finish settles the colour: red on an error or a failed fail-severity assertion; amber on a
// failed warn assertion or a certificate inside its warning window.
func finish(definition spec.Definition, result *spec.RunResult) {
	if result.Error != "" {
		result.Status = spec.StatusDown
		return
	}
	result.Status = spec.StatusUp
	for _, assertion := range result.Assertions {
		if assertion.Passed {
			continue
		}
		if assertion.Severity == spec.SeverityWarn {
			if result.Status == spec.StatusUp {
				result.Status = spec.StatusWarn
			}
			continue
		}
		result.Status = spec.StatusDown
	}
	if result.TLS != nil && result.Status != spec.StatusDown {
		warnDays, failDays := spec.DefaultTLSWarnDays, 0
		if definition.TLS != nil {
			if definition.TLS.WarnDays > 0 {
				warnDays = definition.TLS.WarnDays
			}
			failDays = definition.TLS.FailDays
		}
		switch {
		case result.TLS.DaysLeft <= 0:
			result.Status = spec.StatusDown
			result.Error = "certificate has expired"
		case failDays > 0 && result.TLS.DaysLeft < float64(failDays):
			result.Status = spec.StatusDown
			result.Error = fmt.Sprintf(
				"certificate expires in %.0f days (fail below %d)",
				result.TLS.DaysLeft,
				failDays,
			)
		case result.TLS.DaysLeft < float64(warnDays):
			result.Status = spec.StatusWarn
		}
	}
}

// resolve fills the placeholders the environment policy allows, returning the definition, every
// secret value in it (the control plane's and the environment's) and the names refused.
func (r *Runner) resolve(check spec.ProbeCheck) (spec.Definition, []string, []string) {
	secrets := append([]string{}, check.Redact...)
	names := check.Definition.Placeholders()
	if r.Env == nil || len(names) == 0 {
		return check.Definition, secrets, nil
	}
	var refused []string
	values := map[string]string{}
	for _, name := range names {
		if !r.Env.Allowed(name) {
			refused = append(refused, name)
			continue
		}
		if value, ok := r.Env.Lookup(name); ok {
			values[name] = value
			secrets = append(secrets, value)
		}
	}
	definition := check.Definition.Resolve(func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
	return definition, secrets, refused
}

// resolve looks host up and checks every address against policy, so a
// check never connects somewhere it mustn't.
func resolve(ctx context.Context, policy egress.Policy, host string) ([]net.IP, error) {
	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", host, err)
		}
		for _, address := range addresses {
			ips = append(ips, address.IP)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolved to no addresses", host)
	}
	for _, ip := range ips {
		if err := policy(ip); err != nil {
			return nil, fmt.Errorf("refusing %s: %w", host, err)
		}
	}
	return ips, nil
}

// dialAny connects to the first address that answers, IPv4 first (many networks have no IPv6
// route), each attempt with an equal share of the time left so a black-holed one can't use it all.
func dialAny(ctx context.Context, network string, ips []net.IP, port string) (net.Conn, error) {
	ordered := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip.To4() != nil {
			ordered = append(ordered, ip)
		}
	}
	for _, ip := range ips {
		if ip.To4() == nil {
			ordered = append(ordered, ip)
		}
	}
	dialer := &net.Dialer{KeepAlive: -1}
	var lastErr error
	for i, ip := range ordered {
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			attemptCtx, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(ordered)-i))
		}
		conn, err := dialer.DialContext(attemptCtx, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func probeRefuses(ip net.IP, deny []*net.IPNet) error {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("address %s is not allowed", ip)
	}
	for _, metadata := range metadataAddresses {
		if metadata.Equal(ip) {
			return fmt.Errorf("address %s is not allowed (cloud metadata)", ip)
		}
	}
	for _, network := range deny {
		if network.Contains(ip) {
			return fmt.Errorf("address %s is denied", ip)
		}
	}
	return nil
}

// embeddedIPv4 is the IPv4 address in a NAT64 (64:ff9b::/96), 6to4 (2002::/16) or IPv4-compatible
// (::a.b.c.d) address, else nil; net.IP already treats IPv4-mapped ones as IPv4.
func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len || ip.To4() != nil {
		return nil
	}
	switch {
	case nat64Network.Contains(ip), compatNetwork.Contains(ip) && !ip.IsUnspecified() && !ip.IsLoopback():
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	case sixToFourNetwork.Contains(ip):
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	}
	return nil
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
