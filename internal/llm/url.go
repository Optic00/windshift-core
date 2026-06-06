package llm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"windshift/internal/utils"
)

// joinProviderPath appends an API path to an admin-configured provider base URL.
//
// Many OpenAI-compatible tools document their base URL as .../v1, while the
// built-in Windshift provider definitions keep the host/root URL and store the
// /v1/... suffix in ChatPath/ModelsEndpoint. Accept both forms so a custom
// endpoint entered as http://localhost:11434/v1 does not become
// http://localhost:11434/v1/v1/chat/completions.
func joinProviderPath(baseURL, apiPath string) string {
	base := strings.TrimRight(baseURL, "/")
	path := "/" + strings.TrimLeft(apiPath, "/")
	if base == "" {
		return path
	}
	if strings.HasPrefix(path, "/v1/") && strings.HasSuffix(base, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// newAdminConfiguredHTTPClient returns a client for URLs configured by a system
// administrator. It is SSRF-safe by default; operators must explicitly allow
// each private/loopback CIDR needed for local/internal LLM endpoints.
func newAdminConfiguredHTTPClient(timeout time.Duration, allowedPrivateCIDRs []*net.IPNet) *http.Client {
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	if len(allowedPrivateCIDRs) == 0 {
		return utils.NewSSRFSafeHTTPClient(timeout)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: newLLMSSRFTransport(allowedPrivateCIDRs),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newLLMSSRFTransport(allowedPrivateCIDRs []*net.IPNet) http.RoundTripper {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
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
			if isBlockedLLMAddr(ip, allowedPrivateCIDRs) {
				return fmt.Errorf("%w: %s (%s)", utils.ErrBlockedSSRFAddr, ip.String(), network)
			}
			return nil
		},
	}
	return &http.Transport{DialContext: dialer.DialContext}
}

func isBlockedLLMAddr(ip net.IP, allowedPrivateCIDRs []*net.IPNet) bool {
	if ip == nil {
		return true
	}
	// Never allow these ranges: unspecified binds, link-local metadata surfaces,
	// and multicast are not legitimate LLM endpoints.
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip) {
		return !ipInAllowedCIDRs(ip, allowedPrivateCIDRs)
	}
	return false
}

func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat != nil && cgnat.Contains(v4)
}

func ipInAllowedCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, cidr := range cidrs {
		if cidr != nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}
