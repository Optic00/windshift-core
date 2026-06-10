package handlers

import (
	_ "embed"
	"net/http"
	"strings"

	"windshift/internal/version"
)

// Hosted runner onboarding (WI-313). The orchestrator serves a curl|bash
// install script with everything it already knows baked in — its own public
// /api URL and image references whose tag matches the running server version —
// so the operator supplies only the registration token. Eliminates the
// WS_API_URL / image-name / image-tag guesswork that makes manual runner
// provisioning error-prone.

//go:embed runner_install.sh.tmpl
var runnerInstallScriptTmpl string

const (
	agentImageRepo  = "ghcr.io/windshiftapp/windshift-agent"
	runnerImageRepo = "ghcr.io/windshiftapp/windshift-runner"
)

// RunnerInstallHandler serves GET /runner-install.sh (public, like /version —
// the script contains no secrets, only the public URL and image references).
type RunnerInstallHandler struct {
	// baseURL is the resolved public base URL (cfg.BaseURL or the localhost
	// dev fallback), without the /api suffix.
	baseURL string
}

func NewRunnerInstallHandler(baseURL string) *RunnerInstallHandler {
	return &RunnerInstallHandler{baseURL: baseURL}
}

// runnerImageTag returns the ghcr tag matching this server's build, so the
// generated install command can never hit the :latest-does-not-exist trap on
// unreleased mains (WI-309/WI-314). Release builds are stamped vX.Y.Z and the
// docker workflow publishes the semver tag without the leading v; dev builds
// fall back to :main, the branch tag that always exists.
func runnerImageTag() string {
	if version.Version == "" || version.Version == "dev" {
		return "main"
	}
	return strings.TrimPrefix(version.Version, "v")
}

// apiBaseURL is the public orchestrator base INCLUDING the mandatory /api
// suffix — the exact WS_API_URL a runner must use. Falls back to the request
// host when no base URL is configured (any real deployment sits behind TLS).
// The fallback host is allowlist-validated before being embedded: the script
// is piped straight into bash, so a forged Host header must not be able to
// inject shell syntax.
func (h *RunnerInstallHandler) apiBaseURL(r *http.Request) string {
	base := strings.TrimRight(h.baseURL, "/")
	if base == "" {
		host := r.Host
		if !validScriptHost(host) {
			host = "localhost"
		}
		base = "https://" + host
	}
	return base + "/api"
}

// validScriptHost accepts hostname[:port], IPv4[:port], and bracketed
// IPv6[:port] shapes — nothing that could break out of a quoted shell string.
func validScriptHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

// ServeScript renders the install script with the server-known values
// substituted. GET /runner-install.sh.
func (h *RunnerInstallHandler) ServeScript(w http.ResponseWriter, r *http.Request) {
	api := h.apiBaseURL(r)
	tag := runnerImageTag()
	script := strings.NewReplacer(
		"__WS_API_URL__", api,
		"__SCRIPT_URL__", api+"/runner-install.sh",
		"__AGENT_IMAGE__", agentImageRepo+":"+tag,
		"__RUNNER_IMAGE__", runnerImageRepo+":"+tag,
	).Replace(runnerInstallScriptTmpl)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Not HTML: the body is a shell script (Content-Type above) built from the
	// embedded template + operator config; the only request-derived value is
	// the allowlist-validated fallback host.
	_, _ = w.Write([]byte(script)) //nolint:gosec // G705: shell script response, host validated
}
