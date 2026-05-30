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
	AllowedHosts      string
	AllowedPort       string
	UseProxy          bool
	UseProxyExplicit  bool
	AdditionalProxies string
	TLSCertPath       string
	TLSKeyPath        string
	DisableCSRF       bool

	// Sub-configs grouped by concern
	DB           DBConfig
	SSH          SSHConfig
	Auth         AuthConfig
	SSO          SSOConfig
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

// SSOConfig holds SSO-specific runtime options.
type SSOConfig struct {
	// OIDCAllowedPrivateCIDRs is a comma-separated list of private / CGNAT CIDRs
	// that OIDC discovery, JWKS, and token HTTP calls may dial. Empty keeps the
	// SSRF guard fully public-internet-only.
	OIDCAllowedPrivateCIDRs string
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

// CodingAgentConfig configures the coding-agent harness (WI-89). When
// RunnerImage is non-empty the server constructs a production RunService
// that spawns pi-coding-agent inside that container image, wires it
// through the BindingService, and the assignee-change trigger fires
// real runs. When empty the harness stays in observer mode — bindings
// can still be created, the trigger logs but no-ops.
//
// Sandbox knobs (Network/PidsLimit/Memory/CPUs) layer onto the hardened
// `docker run` defaults baked into DockerPiRunner. They are tunables for
// operator-specific resource budgets, NOT switches that can turn the
// hardening off.
type CodingAgentConfig struct {
	RunnerImage  string // e.g. "windshift/coding-agent:wi-89"
	DockerBinary string // defaults to "docker"
	WorktreeRoot string // absolute host path; required if RunnerImage is set
	GlobalCap    int    // RunService.GlobalCap; defaults to 8
	LLMProvider  string // env LLM_PROVIDER for the container
	LLMModel     string // env LLM_MODEL for the container
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
