package checks

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

const (
	icmpPause      = 200 * time.Millisecond
	minPingTimeout = time.Second
	protocolICMP   = 1
	protocolICMPv6 = 58
)

// UnsupportedICMP is the error a probe without raw or ping sockets reports.
const UnsupportedICMP = "ICMP isn't available on this probe: give it CAP_NET_RAW, or allow ping sockets " +
	"(sysctl net.ipv4.ping_group_range)"

func runICMP(ctx context.Context, policy egress.Policy, definition spec.Definition, result *spec.RunResult) {
	options := definition.ICMP
	ips, err := resolve(ctx, policy, definition.Target)
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.ResolvedIPs = ipStrings(ips)
	target := ips[0]
	v4 := target.To4() != nil
	conn, privileged, err := listenICMP(v4)
	if err != nil {
		result.Unsupported = true
		result.Error = UnsupportedICMP
		return
	}
	defer func() { _ = conn.Close() }()

	var destination net.Addr = &net.UDPAddr{IP: target}
	if privileged {
		destination = &net.IPAddr{IP: target}
	}
	echoType, replyType, protocol := icmp.Type(ipv4.ICMPTypeEcho), icmp.Type(ipv4.ICMPTypeEchoReply), protocolICMP
	if !v4 {
		echoType, replyType, protocol = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply, protocolICMPv6
	}
	perPing := minPingTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if share := time.Until(deadline) / time.Duration(options.Count); share > perPing {
			perPing = share
		}
	}

	id := os.Getpid() & 0xffff
	var rtts []time.Duration
	buffer := make([]byte, 1500)
	for seq := 0; seq < options.Count && ctx.Err() == nil; seq++ {
		message := icmp.Message{Type: echoType, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("obzly-probe")}}
		packet, err := message.Marshal(nil)
		if err != nil {
			result.Error = err.Error()
			return
		}
		sent := time.Now()
		if _, err := conn.WriteTo(packet, destination); err != nil {
			result.Error = "failed to send: " + err.Error()
			return
		}
		_ = conn.SetReadDeadline(sent.Add(perPing))
		for {
			n, from, err := conn.ReadFrom(buffer)
			if err != nil {
				break
			}
			reply, err := icmp.ParseMessage(protocol, buffer[:n])
			if err == nil && isEchoReply(reply, replyType, from, target, privileged, id, seq) {
				rtts = append(rtts, time.Since(sent))
				break
			}
		}
		if seq < options.Count-1 {
			select {
			case <-ctx.Done():
			case <-time.After(icmpPause):
			}
		}
	}

	info := icmpStats(options.Count, rtts)
	result.ICMP = info
	result.DurationMs = info.RTTAvgMs
	result.PhasesMs = map[string]float64{"total": info.RTTAvgMs}
	if info.Received == 0 {
		result.Error = fmt.Sprintf("no replies to %d pings", info.Sent)
		return
	}
	if info.LossRatio > options.MaxLossRatio {
		result.Error = fmt.Sprintf("%.0f%% packet loss (allowed %.0f%%)", info.LossRatio*100, options.MaxLossRatio*100)
		return
	}
	result.Assertions = evaluate(definition.Assertions, 0, info.RTTAvgMs, nil, nil)
	if options.RTTWarnMs > 0 {
		result.Assertions = append(result.Assertions, spec.AssertionResult{
			Description: fmt.Sprintf("Round trip below %d ms", options.RTTWarnMs), Severity: spec.SeverityWarn,
			Passed: info.RTTAvgMs < float64(options.RTTWarnMs), Actual: fmt.Sprintf("%.1f ms", info.RTTAvgMs),
		})
	}
}

// listenICMP prefers an unprivileged ping socket and falls back to a raw
// one (CAP_NET_RAW).
func listenICMP(v4 bool) (*icmp.PacketConn, bool, error) {
	unprivileged, raw, address := "udp4", "ip4:icmp", "0.0.0.0"
	if !v4 {
		unprivileged, raw, address = "udp6", "ip6:ipv6-icmp", "::"
	}
	if conn, err := icmp.ListenPacket(unprivileged, address); err == nil {
		return conn, false, nil
	}
	conn, err := icmp.ListenPacket(raw, address)
	return conn, true, err
}

// isEchoReply: a raw socket sees every echo reply reaching the host, so the sender and the ID
// must match too. Ping sockets get only their own replies but rewrite the ID, so there the
// sequence and sender decide.
func isEchoReply(
	reply *icmp.Message,
	replyType icmp.Type,
	from net.Addr,
	target net.IP,
	privileged bool,
	id, seq int,
) bool {
	echo, ok := reply.Body.(*icmp.Echo)
	if reply.Type != replyType || !ok || echo.Seq != seq || privileged && echo.ID != id {
		return false
	}
	var sender net.IP
	switch address := from.(type) {
	case *net.IPAddr:
		sender = address.IP
	case *net.UDPAddr:
		sender = address.IP
	}
	return sender.Equal(target)
}

func icmpStats(sent int, rtts []time.Duration) *spec.ICMPInfo {
	info := &spec.ICMPInfo{Sent: sent, Received: len(rtts)}
	if sent > 0 {
		info.LossRatio = float64(sent-len(rtts)) / float64(sent)
	}
	if len(rtts) == 0 {
		return info
	}
	var total float64
	for _, rtt := range rtts {
		total += millis(rtt)
	}
	info.RTTAvgMs = total / float64(len(rtts))
	if len(rtts) > 1 {
		var jitter float64
		for i := 1; i < len(rtts); i++ {
			jitter += math.Abs(millis(rtts[i]) - millis(rtts[i-1]))
		}
		info.JitterMs = jitter / float64(len(rtts)-1)
	}
	return info
}
