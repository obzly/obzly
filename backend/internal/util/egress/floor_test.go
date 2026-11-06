package egress

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_PrivateNetworkFloor_RefusesLocalAndMetadata_AllowsPrivateNetworks(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "::1", "0.0.0.0", "::", "0.1.2.3", "169.254.169.254", "fe80::1", "224.0.0.1",
		"fd00:ec2::254", "168.63.129.16", "100.100.100.200",
		// The same, carried in IPv6: NAT64, 6to4, IPv4-compatible, IPv4-mapped.
		"64:ff9b::a9fe:a9fe", "64:ff9b::7f00:1", "2002:a9fe:a9fe::1", "2002:7f00:1::", "::a9fe:a9fe",
		"::127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a83f:8110",
		// Teredo and local-use NAT64 can't be read safely.
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "64:ff9b:1::a00:5",
	} {
		require.Error(t, PrivateNetworkFloor(net.ParseIP(address)), address)
	}
	for _, address := range []string{
		"10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "fd12:3456::1", "192.0.2.10",
		"2606:4700::1111", "64:ff9b::a00:5", "2002:a00:5::1", "::a00:5",
	} {
		require.NoError(t, PrivateNetworkFloor(net.ParseIP(address)), address)
	}
}
