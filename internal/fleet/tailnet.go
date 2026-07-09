package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"
)

// Tailscale assigns IPv4 addresses from the CGNAT block 100.64.0.0/10 and
// IPv6 addresses from fd7a:115c:a1e0::/48.
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

// tailnetStatus is the subset of `tailscale status --json` needed to validate
// a hub listener address.
type tailnetStatus struct {
	BackendState string
	Self         *tailnetNode
}

type tailnetNode struct {
	TailscaleIPs []string
}

// readTailnetStatus queries the local Tailscale daemon. Overridable for
// tests.
var readTailnetStatus = defaultReadTailnetStatus

// verifyTailnetSelfAddress confirms host is one of this machine's own
// tailnet addresses, for validating listen addresses.
func verifyTailnetSelfAddress(host string) error {
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%q is not an IP literal", host)
	}
	status, err := readTailnetStatus()
	if err != nil {
		return err
	}
	if status.BackendState != "Running" {
		return fmt.Errorf("tailscale backend state is %q, not Running", status.BackendState)
	}
	if nodeHasIP(status.Self, ip) {
		return nil
	}
	return fmt.Errorf("%s is not an address of the active tailnet", host)
}

func nodeHasIP(node *tailnetNode, ip net.IP) bool {
	if node == nil {
		return false
	}
	for _, raw := range node.TailscaleIPs {
		if nodeIP := net.ParseIP(raw); nodeIP != nil && nodeIP.Equal(ip) {
			return true
		}
	}
	return false
}

func defaultReadTailnetStatus() (tailnetStatus, error) {
	cli, err := findTailscaleCLI()
	if err != nil {
		return tailnetStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, cli, "status", "--json").Output()
	if err != nil {
		return tailnetStatus{}, fmt.Errorf("run tailscale status: %w", err)
	}
	var status tailnetStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return tailnetStatus{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	return status, nil
}

// findTailscaleCLI locates the tailscale binary. The macOS app bundles the
// CLI inside Tailscale.app without putting it on PATH.
func findTailscaleCLI() (string, error) {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "darwin" {
		candidate := "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("tailscale CLI not found on PATH")
}
