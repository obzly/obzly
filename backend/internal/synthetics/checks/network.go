package checks

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

var tlsVersionNumbers = map[string]uint16{
	"TLS1.0": tls.VersionTLS10, "TLS1.1": tls.VersionTLS11, "TLS1.2": tls.VersionTLS12, "TLS1.3": tls.VersionTLS13,
}

func runTCP(ctx context.Context, policy egress.Policy, definition spec.Definition, result *spec.RunResult) {
	started := time.Now()
	conn, dnsTime, err := dialChecked(ctx, policy, definition.Target, result)
	result.DurationMs = millis(time.Since(started))
	result.PhasesMs = map[string]float64{"dns": millis(dnsTime), "connect": result.DurationMs - millis(dnsTime)}
	if err != nil {
		result.Error = err.Error()
		return
	}
	_ = conn.Close()
	result.Assertions = evaluate(definition.Assertions, 0, result.DurationMs, nil, nil)
}

func runTLS(ctx context.Context, policy egress.Policy, definition spec.Definition, result *spec.RunResult) {
	host, _, _ := net.SplitHostPort(definition.Target)
	options := definition.TLS
	if options == nil {
		options = &spec.TLSOptions{}
	}
	verify := definition.VerifiesTLS()
	checkHostname := options.VerifyHostname == nil || *options.VerifyHostname

	started := time.Now()
	conn, dnsTime, err := dialChecked(ctx, policy, definition.Target, result)
	connected := time.Now()
	if err != nil {
		result.DurationMs = millis(time.Since(started))
		result.Error = err.Error()
		return
	}
	defer func() { _ = conn.Close() }()
	// The handshake always completes; verification is judged afterwards,
	// so an invalid certificate still has its details reported.
	client := tls.Client(
		conn,
		&tls.Config{ServerName: host, MinVersion: tls.VersionTLS10, InsecureSkipVerify: true},
	) //nolint:gosec // verified below
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	err = client.HandshakeContext(ctx)
	finished := time.Now()
	result.DurationMs = millis(finished.Sub(started))
	result.PhasesMs = map[string]float64{
		"dns": millis(
			dnsTime,
		),
		"connect": millis(connected.Sub(started)) - millis(dnsTime),
		"tls":     millis(finished.Sub(connected)),
	}
	if err != nil {
		result.Error = "TLS handshake failed: " + err.Error()
		return
	}
	state := client.ConnectionState()
	result.TLS = tlsInfo(state, host, false, checkHostname)
	if result.TLS == nil {
		result.Error = "the server sent no certificate"
		return
	}
	if verify && !result.TLS.Verified {
		result.Error = "certificate verification failed: " + result.TLS.VerifyError
		return
	}
	if minimum, ok := tlsVersionNumbers[options.MinProtocol]; ok && state.Version < minimum {
		result.Error = fmt.Sprintf(
			"negotiated %s, below the minimum %s",
			tls.VersionName(state.Version),
			options.MinProtocol,
		)
		return
	}
	result.Assertions = evaluate(definition.Assertions, 0, result.DurationMs, nil, nil)
}

func runDNS(ctx context.Context, policy egress.Policy, definition spec.Definition, result *spec.RunResult) {
	options := definition.DNS
	resolver := net.DefaultResolver
	if options.Resolver != "" {
		// The resolver address goes through the policy like any target.
		host, port, err := net.SplitHostPort(options.Resolver)
		if err != nil {
			result.Error = fmt.Sprintf("invalid resolver %q: %v", options.Resolver, err)
			return
		}
		ips, err := resolve(ctx, policy, host)
		if err != nil {
			result.Error = err.Error()
			return
		}
		resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialAny(ctx, network, ips, port)
		}}
	}
	started := time.Now()
	answers, err := lookup(ctx, resolver, options.RecordType, definition.Target)
	result.DurationMs = millis(time.Since(started))
	result.PhasesMs = map[string]float64{"dns": result.DurationMs}
	result.DNSAnswers = answers
	if err != nil {
		result.Error = err.Error()
		return
	}
	if len(answers) == 0 {
		result.Error = fmt.Sprintf("no %s records for %s", options.RecordType, definition.Target)
		return
	}
	if len(options.ExpectAnyOf) > 0 && !slices.ContainsFunc(answers, func(answer string) bool {
		return slices.ContainsFunc(options.ExpectAnyOf, func(expected string) bool {
			return strings.EqualFold(strings.TrimSuffix(expected, "."), answer)
		})
	}) {
		result.Error = fmt.Sprintf("none of the answers (%s) is expected", strings.Join(answers, ", "))
		return
	}
	result.Assertions = evaluate(definition.Assertions, 0, result.DurationMs, nil, nil)
}

func lookup(ctx context.Context, resolver *net.Resolver, recordType, name string) ([]string, error) {
	var answers []string
	switch recordType {
	case "AAAA", "A":
		network := "ip4"
		if recordType == "AAAA" {
			network = "ip6"
		}
		ips, err := resolver.LookupIP(ctx, network, name)
		if err != nil {
			return nil, err
		}
		answers = ipStrings(ips)
	case "CNAME":
		cname, err := resolver.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		answers = []string{strings.TrimSuffix(cname, ".")}
	case "MX":
		records, err := resolver.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			answers = append(answers, strings.TrimSuffix(record.Host, "."))
		}
	case "TXT":
		records, err := resolver.LookupTXT(ctx, name)
		if err != nil {
			return nil, err
		}
		answers = records
	case "NS":
		records, err := resolver.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			answers = append(answers, strings.TrimSuffix(record.Host, "."))
		}
	}
	sort.Strings(answers)
	return answers, nil
}

// dialChecked connects to host:port through policy, recording the
// addresses and how long resolving took.
func dialChecked(
	ctx context.Context,
	policy egress.Policy,
	target string,
	result *spec.RunResult,
) (net.Conn, time.Duration, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid target %q: %w", target, err)
	}
	started := time.Now()
	ips, err := resolve(ctx, policy, host)
	dnsTime := time.Since(started)
	if err != nil {
		return nil, dnsTime, err
	}
	result.ResolvedIPs = ipStrings(ips)
	conn, err := dialAny(ctx, "tcp", ips, port)
	return conn, dnsTime, err
}
