package config

import (
	"embed"
	"os"
	"time"
)

// Config is the fully-resolved runtime configuration for the main windshift
// server. It is produced by Load (reading flags + env) and consumed by
// server.New and every handler that previously read env vars directly.
// last review: ser, 210426
type Config struct {
	// HTTP / network
	Port              string
	BaseURL           string
	ContextPath       string
	AllowedHosts      string
	AllowedPort       string
	UseProxy          bool
	UseProxyExplicit  bool
	AdditionalProxies string
	TLSCertPath       string
	TLSKeyPath        string
	DisableCSRF       bool

	// AllowLocalConnections, when true, lets every server-side SSRF-safe HTTP
	// client/dialer reach loopback and private/RFC1918 destinations. It is the
	// single switch operators flip to run self-hosted SCM (Gitea / GitHub
	// Enterprise), Jira Data Center, or a local LLM gateway on a private
	// network — instead of allowlisting each endpoint's CIDR. Off by default.
	AllowLocalConnections bool

	// Sub-configs grouped by concern
	DB           DBConfig
	SSH          SSHConfig
	Auth         AuthConfig
	WebAuthn     WebAuthnConfig
	Logging      LoggingConfig
	Plugins      PluginsConfig
	LLM          LLMConfig
	CodingAgent  CodingAgentConfig
	Logbook      LogbookConfig
	Notification NotificationConfig
	Jira         JiraConfig

	// Flat fields (no logical grouping)
	AttachmentPath      string
	EnableAdminFallback bool
	DisableIPRateLimit  bool
	MCPEnabled          bool
	RecoverUser         string

	// Wire-time values (not env/flag-driven)
	FrontendFiles embed.FS
	ShutdownChan  chan os.Signal
	SilentMode    bool
}

// DBConfig holds database connection + pool configuration.
type DBConfig struct {
	// PostgresConn, when non-empty, selects Postgres; otherwise SQLitePath is used.
	PostgresConn  string
	SQLitePath    string
	MaxReadConns  int
	MaxWriteConns int
}

// SSHConfig holds the SSH TUI server configuration.
type SSHConfig struct {
	Enabled bool
	Host    string
	Port    string
	KeyPath string
}

// AuthConfig holds session-signing / SSO credential-encryption secrets.
type AuthConfig struct {
	// SessionSecret is resolved from SSO_SECRET (preferred) with fallback to
	// SESSION_SECRET. Empty means neither env var was set — Load will fatal
	// before populating this field, so consumers can treat empty as unreachable.
	SessionSecret string
}

// WebAuthnConfig holds WebAuthn relying-party identity.
type WebAuthnConfig struct {
	// RPID is the relying party ID (usually the hostname). In production it is
	// resolved from WEBAUTHN_RP_ID with fallback to os.Hostname(). In
	// development the webauthn package overrides this to "localhost".
	RPID string
	// RPName is the displayed RP name; defaults to "Windshift".
	RPName string
}

// LoggingConfig holds logger initialization parameters.
type LoggingConfig struct {
	Level  string // debug, info, warn, error
	Format string // text, json, logfmt
}

// PluginsConfig holds plugin-system configuration.
type PluginsConfig struct {
	Disabled  bool
	Dir       string
	ExtraDirs []string // from PLUGIN_DIRS (comma-separated)
}

// LLMConfig holds LLM-related configuration.
type LLMConfig struct {
	Endpoint      string
	ProvidersFile string
	PromptsDir    string
}

// DefaultCodingAgentRunnerImage is the windshift-agent image the in-process
// runner spawns when CODING_AGENT_RUNNER_IMAGE is unset. The image name is a
// standard, non-host-specific value, so it lives in the binary; operators only
// override it to pin a custom build.
const DefaultCodingAgentRunnerImage = "ghcr.io/windshiftapp/windshift-agent:latest"

// DefaultCodingAgentWorktreeRoot is where the in-process runner prepares per-run
// checkouts when CODING_AGENT_WORKTREE_ROOT is unset — under the conventional
// container data dir. Override for a different host layout.
const DefaultCodingAgentWorktreeRoot = "/data/worktrees"

// CodingAgentConfig configures the coding-agent harness (WI-89). RunnerImage and
// WorktreeRoot both carry built-in defaults (DefaultCodingAgentRunnerImage and
// DefaultCodingAgentWorktreeRoot), so the harness is active out of the box: the
// server constructs a production RunService that spawns the windshift-agent
// harness (the node-free codehamr fork, WI-204) inside RunnerImage, wires it
// through the BindingService, and the assignee-change trigger fires real runs.
//
// The activation gate is still WorktreeRoot != "" — left in place so a future
// remote-only control plane can opt out of the in-process runner — but since it
// defaults non-empty, the in-process runner is on unless that default is
// explicitly cleared. Override CODING_AGENT_RUNNER_IMAGE to pin a custom agent
// build and CODING_AGENT_WORKTREE_ROOT to relocate the per-run checkouts.
//
// The agent reaches the model only through the llm-proxy broker, so no
// provider key or provider selection is injected into the container; it
// needs only an LLM_BASE_URL (set per-run to the run-scoped proxy) and a
// model id.
//
// Sandbox knobs (Network/PidsLimit/Memory/CPUs) layer onto the hardened
// `docker run` defaults baked into DockerAgentRunner. They are tunables
// for operator-specific resource budgets, NOT switches that can turn the
// hardening off.
type CodingAgentConfig struct {
	RunnerImage  string // e.g. "ghcr.io/windshiftapp/windshift-agent:latest"
	DockerBinary string // defaults to "docker"
	WorktreeRoot string // absolute host path; required if RunnerImage is set
	GlobalCap    int    // RunService.GlobalCap; defaults to 8
	LLMModel     string // fallback env LLM_MODEL for the container when a binding has no llm_connection_id
	WSAPIURL     string // URL the runner container uses to reach this Windshift API; defaults to BASE_URL
	Network      string // docker --network value; defaults to "coding-agent-egress" (operator-created, egress-filtered)
	PidsLimit    int    // docker --pids-limit; defaults to 512
	Memory       string // docker --memory + --memory-swap; defaults to "4g"
	CPUs         string // docker --cpus; defaults to "2"
}

// LogbookConfig holds the URL of the logbook sidecar (if any).
type LogbookConfig struct {
	Endpoint string
}

// NotificationConfig holds the notification WriteBatcher tuning knobs.
type NotificationConfig struct {
	FlushInterval time.Duration
	BatchSize     int
	SyncInterval  time.Duration
	// BatchInterval is the email-batch scheduler cadence
	// (WINDSHIFT_NOTIFICATION_BATCH_INTERVAL). Zero means the scheduler uses
	// its built-in default.
	BatchInterval time.Duration
}

// JiraConfig holds Jira-related runtime options.
type JiraConfig struct {
	// CapturePayloadsDir, when non-empty, makes the Jira import path write
	// request/response payloads to this directory for debugging.
	CapturePayloadsDir string
}

// LogbookSidecarConfig is the sibling of Config for the standalone logbook
// sidecar binary (cmd/logbook/main.go). The sidecar never parses CLI flags
// and has a narrower set of concerns than the main server.
type LogbookSidecarConfig struct {
	PostgresConn     string
	Port             string
	StoragePath      string
	LLMEndpoint      string
	ArticleEndpoint  string
	MainServerURL    string
	MainServerSecret string
	BaseURL          string
	Logging          LoggingConfig
}
