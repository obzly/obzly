package egress

import (
	"fmt"
	"net"
)

// Cloud metadata and host-agent endpoints outside link-local: AWS IMDS over
// IPv6, Azure's WireServer, Alibaba Cloud's metadata service.
var metadataNetworks = []*net.IPNet{
	mustCIDR("fd00:ec2::254/128"), mustCIDR("168.63.129.16/32"), mustCIDR("100.100.100.200/32"),
}

var (
	ipv4CompatibleNetwork = mustCIDR("::/96")
	thisNetwork           = mustCIDR("0.0.0.0/8")
)

// PrivateNetworkFloor is the policy for addresses that may legitimately be
// on a private network (a control plane's Sites): RFC 1918, ULA and CGNAT
// are allowed, but never loopback, link-local, unspecified, multicast,
// 0.0.0.0/8 or a cloud metadata endpoint - nor an IPv6 address carrying one
// of those as an IPv4 address (NAT64, 6to4, IPv4-compatible), nor Teredo or
// local-use NAT64, which can't be read safely.
func PrivateNetworkFloor(ip net.IP) error {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || thisNetwork.Contains(ip) {
		return fmt.Errorf("address %s is not allowed", ip)
	}
	for _, network := range metadataNetworks {
		if network.Contains(ip) {
			return fmt.Errorf("address %s is a metadata endpoint", ip)
		}
	}
	if len(ip) != net.IPv6len {
		return nil
	}
	embedded := embeddedIPv4(ip)
	if embedded == nil && ipv4CompatibleNetwork.Contains(ip) {
		embedded = net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	}
	if embedded != nil {
		if err := PrivateNetworkFloor(embedded); err != nil {
			return fmt.Errorf("address %s (%s) is not allowed", ip, embedded)
		}
		return nil
	}
	for _, network := range opaqueIPv4Networks {
		if network.Contains(ip) {
			return fmt.Errorf("address %s embeds an IPv4 address", ip)
		}
	}
	return nil
}
