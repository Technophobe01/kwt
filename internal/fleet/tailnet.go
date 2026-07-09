package fleet

import (
	"net"
	"strings"
)

// Tailscale encrypts all tailnet traffic end-to-end with WireGuard, so plain
// HTTP between tailnet peers is never cleartext on the wire. That makes
// tailnet hosts safe targets for the plaintext hub rules that otherwise
// require loopback. Tailscale assigns IPv4 addresses from the CGNAT block
// 100.64.0.0/10, IPv6 addresses from fd7a:115c:a1e0::/48, and MagicDNS names
// under ts.net (resolved locally by tailscaled, not upstream DNS).
var (
	tailnetIPv4Block = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}
	tailnetIPv6Block = &net.IPNet{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)}
)

// isTailnetHost reports whether host (an IP literal or hostname, without
// port or brackets) addresses a Tailscale peer.
func isTailnetHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if strings.HasSuffix(host, ".ts.net") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return tailnetIPv4Block.Contains(ip) || tailnetIPv6Block.Contains(ip)
}
