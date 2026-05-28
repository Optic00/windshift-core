package utils

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedSSRFAddr is returned by SafeNetDialer when the resolved IP
// for a connection is in a non-public range (loopback, RFC1918, link-local,
// CGNAT, multicast, unspecified). Callers can errors.Is(err, ErrBlockedSSRFAddr)
// to surface a clean error to admins instead of leaking dial details.
var ErrBlockedSSRFAddr = errors.New("dial host resolves to a blocked IP range")

// IsBlockedSSRFAddr reports whether ip belongs to a range that user-controlled
// dialing must never reach. It is the conservative superset of IsPrivateIP:
// also rejects unspecified, multicast, and CGNAT 100.64.0.0/10 (covers cloud
// metadata endpoints and other internal-only ranges that some
// IsPrivate-equivalents miss).
func IsBlockedSSRFAddr(ip net.IP) bool {
	return IsBlockedSSRFAddrWithAllowedCIDRs(ip, nil)
}

// IsBlockedSSRFAddrWithAllowedCIDRs is IsBlockedSSRFAddr plus an explicit
// operator allowlist for private / CGNAT networks. The allowlist intentionally
// cannot override loopback, unspecified, link-local, or multicast blocks:
// those ranges include local process surfaces and cloud metadata endpoints and
// should not be reachable by server-side, config-driven HTTP clients.
func IsBlockedSSRFAddrWithAllowedCIDRs(ip net.IP, allowedCIDRs []*net.IPNet) bool {
	if ip == nil {
		return true
	}
	if isAlwaysBlockedSSRFAddr(ip) {
		return true
	}
	if isPrivateOrCGNAT(ip) {
		return !ipInCIDRs(ip, allowedCIDRs)
	}
	return false
}

func isAlwaysBlockedSSRFAddr(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func isPrivateOrCGNAT(ip net.IP) bool {
	if ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
		return cgnat != nil && cgnat.Contains(v4)
	}
	return false
}

func ipInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, cidr := range cidrs {
		if cidr != nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseCIDRList parses a comma-separated list of CIDRs. Bare IP literals are
// accepted as host routes (/32 for IPv4, /128 for IPv6) for operator
// convenience when allowing a single trusted endpoint.
func ParseCIDRList(value string) ([]*net.IPNet, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	cidrs := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, cidr, err := net.ParseCIDR(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
			}
			cidrs = append(cidrs, cidr)
			continue
		}

		ip := net.ParseIP(part)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP/CIDR %q", part)
		}
		bits := 128
		if v4 := ip.To4(); v4 != nil {
			ip = v4
			bits = 32
		}
		cidrs = append(cidrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return cidrs, nil
}

// SafeNetDialer returns a *net.Dialer that refuses to connect to non-public IPs.
// It uses ControlContext to inspect the resolved address *after* DNS resolution
// but *before* the TCP handshake, so the check is robust against DNS rebinding.
//
// Use this anywhere a config-driven host (SMTP server, IMAP server, webhook
// target, etc.) is dialed by the server: a malicious config setter could
// otherwise point the dialer at 127.0.0.1, 169.254.169.254 (cloud metadata),
// or RFC1918 internal services. The blocklist mirrors IsBlockedSSRFAddr above.
func SafeNetDialer(timeout time.Duration) *net.Dialer {
	return SafeNetDialerWithAllowedCIDRs(timeout, nil)
}

// SafeNetDialerWithAllowedCIDRs returns a SafeNetDialer that permits private /
// CGNAT destination IPs only when they fall inside allowedCIDRs. See
// IsBlockedSSRFAddrWithAllowedCIDRs for the ranges that remain blocked even
// when listed.
func SafeNetDialerWithAllowedCIDRs(timeout time.Duration, allowedCIDRs []*net.IPNet) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		ControlContext: func(_ context.Context, network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("invalid dial address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("dial host %q did not resolve to an IP", host)
			}
			if IsBlockedSSRFAddrWithAllowedCIDRs(ip, allowedCIDRs) {
				return fmt.Errorf("%w: %s (%s)", ErrBlockedSSRFAddr, ip.String(), network)
			}
			return nil
		},
	}
}
