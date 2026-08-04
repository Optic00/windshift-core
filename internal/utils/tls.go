package utils

import (
	"crypto/tls"
	"net/http"
	"sync/atomic"
	"time"
)

// skipTLSVerify is process-wide because outbound clients are constructed
// across integration packages. Server startup sets it once from the resolved
// TLS_SKIP_VERIFY environment setting before any of those clients are built.
var skipTLSVerify atomic.Bool

// SetSkipTLSVerify configures certificate verification for outbound TLS.
// Enabling it accepts certificates without validating their chain or hostname.
func SetSkipTLSVerify(enabled bool) { skipTLSVerify.Store(enabled) }

// SkipTLSVerify reports whether outbound certificate verification is disabled.
func SkipTLSVerify() bool { return skipTLSVerify.Load() }

// OutboundTLSConfig returns the shared TLS policy for raw protocol clients such
// as SMTP and IMAP. An empty serverName lets a higher-level HTTP transport fill
// it from the request URL.
func OutboundTLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: SkipTLSVerify(), //nolint:gosec // Explicit TLS_SKIP_VERIFY operator opt-in.
	}
}

// ConfigureHTTPTransport applies the shared outbound TLS policy to a newly
// constructed HTTP transport while preserving its dialer, proxy, and timeout
// settings.
func ConfigureHTTPTransport(transport *http.Transport) *http.Transport {
	if transport == nil {
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = defaultTransport.Clone()
		} else {
			transport = &http.Transport{}
		}
	}
	tlsConfig := OutboundTLSConfig("")
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
		tlsConfig.InsecureSkipVerify = SkipTLSVerify() //nolint:gosec // Explicit TLS_SKIP_VERIFY operator opt-in.
	}
	transport.TLSClientConfig = tlsConfig
	return transport
}

// NewHTTPClient returns an HTTP client using the shared outbound TLS policy.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ConfigureHTTPTransport(nil),
	}
}
