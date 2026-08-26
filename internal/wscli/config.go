package wscli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config represents the CLI configuration
type Config struct {
	Server        ServerConfig      `toml:"server"`
	Defaults      DefaultsConfig    `toml:"defaults"`
	Cache         CacheConfig       `toml:"cache"`
	StatusAliases map[string]string `toml:"status_aliases"`
}

type ServerConfig struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

type DefaultsConfig struct {
	WorkspaceKey string `toml:"workspace_key"`
}

type CacheConfig struct {
	UserID int `toml:"user_id"`
}

var cfg Config

// discoveredProjectConfig is the ws.toml path actually loaded for this
// invocation (either an explicit --config, or the nearest ws.toml found by
// walking up from cwd). Empty when no project config was found. Other
// subcommands (config show, config refresh) read this so they target the
// real file instead of a stray ./ws.toml in a subdirectory.
var discoveredProjectConfig string

func initConfig() {
	// Initialize config with defaults
	cfg = Config{
		StatusAliases: make(map[string]string),
	}
	discoveredProjectConfig = ""

	// 1. Load global config first (lowest priority)
	globalConfigPath := getGlobalConfigPath()
	if _, err := os.Stat(globalConfigPath); err == nil {
		loadConfigFile(globalConfigPath)
	}

	// 2. Load project config (overrides global). Walk up from cwd so that
	// running `ws` from a subdirectory of the repo still picks up the
	// repo-root ws.toml instead of silently falling back to the global token.
	projectConfigPath := cfgFile
	if projectConfigPath == "" {
		projectConfigPath = findProjectConfigPath()
	}
	if projectConfigPath != "" {
		if _, err := os.Stat(projectConfigPath); err == nil {
			loadConfigFile(projectConfigPath)
			discoveredProjectConfig = projectConfigPath
		}
	}

	// 3. Override with environment variables
	if envURL := os.Getenv("WS_URL"); envURL != "" {
		cfg.Server.URL = envURL
	}
	if envToken := os.Getenv("WS_TOKEN"); envToken != "" {
		cfg.Server.Token = envToken
	}
	if envWorkspace := os.Getenv("WS_WORKSPACE"); envWorkspace != "" {
		cfg.Defaults.WorkspaceKey = envWorkspace
	}

	// 4. Override with CLI flags (highest priority)
	if serverURL != "" {
		cfg.Server.URL = serverURL
	}
	if token != "" {
		cfg.Server.Token = token
	}
	if workspaceKey != "" {
		cfg.Defaults.WorkspaceKey = workspaceKey
	}
}

func loadConfigFile(path string) {
	var fileCfg Config
	if _, err := toml.DecodeFile(path, &fileCfg); err != nil {
		return
	}

	// Merge file config into main config
	if fileCfg.Server.URL != "" {
		cfg.Server.URL = fileCfg.Server.URL
	}
	if fileCfg.Server.Token != "" {
		cfg.Server.Token = fileCfg.Server.Token
	}
	if fileCfg.Defaults.WorkspaceKey != "" {
		cfg.Defaults.WorkspaceKey = fileCfg.Defaults.WorkspaceKey
	}
	if fileCfg.Cache.UserID != 0 {
		cfg.Cache.UserID = fileCfg.Cache.UserID
	}
	for k, v := range fileCfg.StatusAliases {
		if warning := validateAliasValue(k, v); warning != "" {
			_, _ = fmt.Fprintf(stderr, "warning: %s in %s — alias ignored\n", warning, path)
			continue
		}
		cfg.StatusAliases[k] = v
	}
}

// validateAliasValue rejects likely packed TOML aliases. Numeric IDs and names
// are valid; comma/equal-sign values are reported as hand-edit mistakes.
func validateAliasValue(key, value string) string {
	if strings.ContainsAny(value, ",=") {
		return fmt.Sprintf("status_aliases.%s = %q looks malformed (contains , or =) — split into separate keys", key, value)
	}
	return ""
}

// findProjectConfigPath walks up from the current working directory looking
// for a ws.toml. Returns the absolute path of the first match, or "" if none
// is found before reaching the filesystem root.
func findProjectConfigPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "ws.toml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func getGlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ws", "config.toml")
}

func saveGlobalConfig(config Config) error {
	path := getGlobalConfigPath()
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // private directories require owner execute permission
		return fmt.Errorf("failed to protect config directory: %w", err)
	}
	return writeConfigAtomically(path, config)
}

func saveProjectConfig(config Config, path string) error {
	return writeConfigAtomically(path, config)
}

func writeConfigAtomically(path string, config Config) error {
	return writeFileAtomically(path, func(w io.Writer) error {
		return toml.NewEncoder(w).Encode(config)
	})
}

func writeFileAtomically(path string, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp") //nolint:gosec // destination directory is selected by the CLI user
	if err != nil {
		return fmt.Errorf("failed to create temporary config file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to protect temporary config file: %w", err)
	}
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to encode config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync temporary config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil { //nolint:gosec // both paths are in the caller-selected config directory
		return fmt.Errorf("failed to replace config file: %w", err)
	}
	return nil
}

// ResolveStatus resolves a status input using aliases, falling back to the input itself
func (c *Config) ResolveStatus(input string) string {
	if resolved, ok := c.StatusAliases[input]; ok {
		return resolved
	}
	return input
}

// ResolveStatusWithFallback resolves a status input, falling back to the completed-statuses
// endpoint when the alias is non-numeric (stale) or when "done" has no alias.
// Returns comma-separated IDs for completed statuses, or the resolved value.
func (c *Config) ResolveStatusWithFallback(input string, client *Client) string {
	resolved := c.ResolveStatus(input)

	// If already numeric, use it directly
	if _, err := fmt.Sscanf(resolved, "%d", new(int)); err == nil {
		return resolved
	}

	// Non-numeric resolution — try completed-statuses endpoint for "done" alias
	if input == "done" || resolved == "done" {
		wsKey := c.GetEffectiveWorkspace()
		if wsKey == "" {
			return resolved
		}
		wsID, err := client.ResolveWorkspaceID(wsKey)
		if err != nil {
			return resolved
		}
		statuses, err := client.GetCompletedStatuses(wsID)
		if err != nil || len(statuses) == 0 {
			return resolved
		}
		ids := make([]string, 0, len(statuses))
		for _, s := range statuses {
			ids = append(ids, fmt.Sprintf("%d", s.ID))
		}
		return strings.Join(ids, ",")
	}

	return resolved
}

// GetEffectiveWorkspace returns the workspace key to use for queries
func (c *Config) GetEffectiveWorkspace() string {
	return c.Defaults.WorkspaceKey
}
