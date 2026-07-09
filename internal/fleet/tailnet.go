package fleet

import "net"

// Tailscale encrypts all tailnet traffic end-to-end with WireGuard, so plain
// HTTP between tailnet peers is never cleartext on the wire. Tailscale
// assigns IPv4 addresses from the CGNAT block 100.64.0.0/10 and IPv6
// addresses from fd7a:115c:a1e0::/48.
var (
	tailnetIPv4Block = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}
	tailnetIPv6Block = &net.IPNet{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)}
)

// isTailnetIP reports whether host is an IP literal inside the Tailscale
// address ranges. Hostnames — including *.ts.net MagicDNS names — are
// deliberately not accepted: DNS resolution is unauthenticated, so a name is
// no evidence the connection terminates on a tailnet peer.
func isTailnetIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return tailnetIPv4Block.Contains(ip) || tailnetIPv6Block.Contains(ip)
}

// hasTailnetInterface reports whether this machine holds a tailnet-range
// address on an active non-loopback interface — evidence that tailscaled is
// up and owns routing for the tailnet ranges. Without it, packets to a
// tailnet-range destination would follow the default route in cleartext.
// Overridable for tests.
var hasTailnetInterface = defaultHasTailnetInterface

func defaultHasTailnetInterface() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if tailnetIPv4Block.Contains(ipNet.IP) || tailnetIPv6Block.Contains(ipNet.IP) {
				return true
			}
		}
	}
	return false
}
