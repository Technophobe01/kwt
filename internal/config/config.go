// Package config provides configuration management for the kwt application.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.kenn.io/kwt/internal/utils"
	"go.kenn.io/kwt/pkg/models"
)

const (
	configName      = "config"
	configType      = "toml"
	localConfigName = ".kwt"

	// DefaultNamingTemplate is the starter naming template used for new global configs.
	DefaultNamingTemplate = "{{.FullPath}}/{{.Branch}}"

	legacyDefaultNamingTemplate = "{{.Host}}/{{.Owner}}/{{.Repository}}/{{.Branch}}"
)

func defaultAgents() map[string]string {
	return map[string]string{
		"codex":   "codex",
		"claude":  "claude",
		"roborev": "roborev tui",
	}
}

func defaultLayouts() models.LayoutsConfig {
	panes := []string{"agent:codex", "agent:claude", "agent:roborev", ""}
	return models.LayoutsConfig{
		Default:         "quad",
		AutoLaunchOnAdd: true,
		Presets: []models.Layout{
			{Name: "quad", Arrange: "even-horizontal", Panes: panes},
			{Name: "grid", Arrange: "tiled", Panes: panes},
			{Name: "focus", Arrange: "main-vertical", Panes: panes},
			{Name: "stack", Arrange: "even-vertical", Panes: panes},
		},
	}
}

// getConfigDir returns the configuration directory path.
func getConfigDir() string {
	if kwtHome := os.Getenv("KWT_HOME"); kwtHome != "" {
		if expanded, err := utils.ExpandPath(kwtHome); err == nil {
			return expanded
		}
		return kwtHome
	}

	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home is not available
		return filepath.Join(".", ".config", "kwt")
	}
	return filepath.Join(home, ".config", "kwt")
}

// getLocalConfigPath returns the path to the local config file if it exists.
// Returns empty string if no local config is found.
func getLocalConfigPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	localConfigPath := filepath.Join(cwd, localConfigName+"."+configType)
	if _, err := os.Stat(localConfigPath); os.IsNotExist(err) {
		return ""
	}

	return localConfigPath
}

// mergeLocalConfig reads the local `.kwt.toml` from CWD and merges it into global viper.
// Local config is treated as untrusted: it is only merged after the trust store confirms
// (absolute path, sha256) match, or the user explicitly grants trust via the prompter.
//
//   - Non-regular files (directory / FIFO / socket / device) are skipped with a stderr warning.
//   - If interactive is false (non-TTY), unknown files are skipped with a stderr warning.
//   - On user rejection, the file is skipped (command continues, global config only).
//   - Trust store write failures are non-fatal (merge proceeds with a stderr warning).
//
// For repository_settings, merging is done by the `repository` field as the key.
func mergeLocalConfig(store *TrustStore, prompter trustPrompter, interactive bool) error {
	rawPath := getLocalConfigPath()
	if rawPath == "" {
		return nil
	}

	absPath, err := normalizeConfigPath(rawPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kwt: skipping local config: %v\n", err)
		return nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read local config %s: %w", absPath, err)
	}
	sum := computeSHA256(data)

	if store == nil {
		store, err = LoadTrustStore(defaultTrustStorePath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "kwt: failed to load trust store (continuing empty): %v\n", err)
			store = &TrustStore{path: defaultTrustStorePath()}
		}
	}

	if !store.IsTrusted(absPath, sum) {
		if !interactive {
			fmt.Fprintf(os.Stderr, "kwt: skipping untrusted local config %s (non-interactive session)\n", absPath)
			return nil
		}
		granted, err := prompter.PromptTrust(absPath, data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kwt: trust prompt failed, skipping local config: %v\n", err)
			return nil
		}
		if !granted {
			fmt.Fprintf(os.Stderr, "kwt: local config %s not trusted, skipping\n", absPath)
			return nil
		}
		if err := store.Add(absPath, sum); err != nil {
			fmt.Fprintf(os.Stderr, "kwt: failed to persist trust decision (continuing): %v\n", err)
		}
	}

	localViper := viper.New()
	localViper.SetConfigType(configType)
	if err := localViper.ReadConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("parse local config %s: %w", absPath, err)
	}

	for _, key := range localViper.AllKeys() {
		switch key {
		case "repository_settings":
			mergeRepositorySettings(localViper)
		case "projects":
			continue
		default:
			viper.Set(key, localViper.Get(key))
		}
	}

	return nil
}

// mergeRepositorySettings merges repository_settings from local config into global config.
// The "repository" field is used as the key for merging:
// - Same repository: local overrides global
// - Different repository: both are kept
func mergeRepositorySettings(localViper *viper.Viper) {
	var globalSettings, localSettings []models.RepositorySetting

	if err := viper.UnmarshalKey("repository_settings", &globalSettings); err != nil {
		globalSettings = nil
	}

	if err := localViper.UnmarshalKey("repository_settings", &localSettings); err != nil {
		return
	}

	localMap := make(map[string]models.RepositorySetting, len(localSettings))
	for _, ls := range localSettings {
		localMap[ls.Repository] = ls
	}

	merged := make([]models.RepositorySetting, 0, len(globalSettings)+len(localSettings))
	overridden := make(map[string]bool, len(localSettings))

	for _, gs := range globalSettings {
		if ls, exists := localMap[gs.Repository]; exists {
			merged = append(merged, ls)
			overridden[gs.Repository] = true
		} else {
			merged = append(merged, gs)
		}
	}

	for _, ls := range localSettings {
		if !overridden[ls.Repository] {
			merged = append(merged, ls)
		}
	}

	viper.Set("repository_settings", merged)
}

// Init initializes the configuration system, creating default config if needed.
func Init() error {
	configDir := getConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	configPath := filepath.Join(configDir, configName+"."+configType)

	viper.SetConfigName(configName)
	viper.SetConfigType(configType)
	viper.AddConfigPath(configDir)

	viper.SetDefault("cd.launch_shell", true)
	viper.SetDefault("worktree.basedir", "~/worktrees")
	viper.SetDefault("worktree.auto_mkdir", true)
	viper.SetDefault("finder.preview", true)
	viper.SetDefault("ui.icons", true)
	viper.SetDefault("ui.tilde_home", true)

	// Naming defaults
	viper.SetDefault("naming.template", DefaultNamingTemplate)
	viper.SetDefault("naming.sanitize_chars", map[string]string{
		"/": "-",
		":": "-",
	})

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			if err := writeDefaultConfig(configPath); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}
			if err := viper.ReadInConfig(); err != nil {
				return fmt.Errorf("failed to read created config: %w", err)
			}
		} else {
			return fmt.Errorf("failed to read config: %w", err)
		}
	}

	migrated, err := backfillWorkspaceConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to migrate workspace config: %w", err)
	}
	if migrated {
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("failed to read migrated config: %w", err)
		}
	}

	return nil
}

func backfillWorkspaceConfig(configPath string) (bool, error) {
	globalViper := viper.New()
	globalViper.SetConfigFile(configPath)
	globalViper.SetConfigType(configType)
	if err := globalViper.ReadInConfig(); err != nil {
		return false, err
	}

	migrated := false
	agents := globalViper.GetStringMapString("agents")
	if agents == nil {
		agents = make(map[string]string)
	}
	for name, command := range defaultAgents() {
		if strings.TrimSpace(agents[name]) == "" {
			agents[name] = command
			migrated = true
		}
	}
	if migrated {
		globalViper.Set("agents", agents)
	}

	if strings.TrimSpace(globalViper.GetString("naming.template")) == legacyDefaultNamingTemplate {
		globalViper.Set("naming.template", DefaultNamingTemplate)
		migrated = true
	}

	var layouts models.LayoutsConfig
	_ = globalViper.UnmarshalKey("layouts", &layouts)
	defaults := defaultLayouts()
	if strings.TrimSpace(layouts.Default) == "" {
		globalViper.Set("layouts.default", defaults.Default)
		migrated = true
	}
	if !globalViper.IsSet("layouts.auto_launch_on_add") {
		globalViper.Set("layouts.auto_launch_on_add", defaults.AutoLaunchOnAdd)
		migrated = true
	}
	if len(layouts.Presets) == 0 {
		globalViper.Set("layouts.presets", defaults.Presets)
		migrated = true
	}

	if !migrated {
		return false, nil
	}
	if err := globalViper.WriteConfigAs(configPath); err != nil {
		return false, err
	}
	return true, nil
}

func writeDefaultConfig(configPath string) (err error) {
	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = file.WriteString(defaultConfigTOML())
	return err
}

func defaultConfigTOML() string {
	return fmt.Sprintf(`[worktree]
basedir = "~/worktrees"
auto_mkdir = true

[cd]
launch_shell = true

[finder]
preview = true

[ui]
icons = true
tilde_home = true

[naming]
template = "%s"

[naming.sanitize_chars]
"/" = "-"
":" = "-"

[agents]
codex = "codex"
claude = "claude"
roborev = "roborev tui"

[layouts]
default = "quad"
auto_launch_on_add = true

[[layouts.presets]]
name = "quad"
arrange = "even-horizontal"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]

[[layouts.presets]]
name = "grid"
arrange = "tiled"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]

[[layouts.presets]]
name = "focus"
arrange = "main-vertical"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]

[[layouts.presets]]
name = "stack"
arrange = "even-vertical"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]
`, DefaultNamingTemplate)
}

// MergeCwdLocal merges the trust-gated cwd `.kwt.toml` into the global config.
// It is called explicitly by commands that operate on the current repository
// (via the root PersistentPreRunE); global/cross-project commands such as
// `open` skip it so a caller's cwd config never leaks into another worktree.
func MergeCwdLocal() error {
	if err := mergeLocalConfig(nil, newStdioPrompter(), isStdinInteractive()); err != nil {
		return fmt.Errorf("failed to merge local config: %w", err)
	}
	return nil
}

// LoadRepoLayoutDefault reads <repoRoot>/.kwt.toml (trust-gated, in isolation)
// and returns its layouts.default. Returns "" when the file is absent,
// untrusted in a non-interactive session, declined, or has no default. It does
// not touch the global viper singleton, so it is safe for cross-project use by
// `open`.
func LoadRepoLayoutDefault(repoRoot string, interactive bool) (string, error) {
	path := filepath.Join(repoRoot, localConfigName+"."+configType)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	absPath, err := normalizeConfigPath(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kwt: skipping target config: %v\n", err)
		return "", nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read target config %s: %w", absPath, err)
	}
	sum := computeSHA256(data)

	store, err := LoadTrustStore(defaultTrustStorePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kwt: failed to load trust store (continuing empty): %v\n", err)
		store = &TrustStore{path: defaultTrustStorePath()}
	}
	if !store.IsTrusted(absPath, sum) {
		if !interactive {
			fmt.Fprintf(os.Stderr, "kwt: skipping untrusted target config %s (non-interactive)\n", absPath)
			return "", nil
		}
		granted, err := newStdioPrompter().PromptTrust(absPath, data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kwt: trust prompt failed, skipping target config: %v\n", err)
			return "", nil
		}
		if !granted {
			return "", nil
		}
		if err := store.Add(absPath, sum); err != nil {
			fmt.Fprintf(os.Stderr, "kwt: failed to persist trust decision (continuing): %v\n", err)
		}
	}

	lv := viper.New()
	lv.SetConfigType(configType)
	if err := lv.ReadConfig(bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("parse target config %s: %w", absPath, err)
	}
	return lv.GetString("layouts.default"), nil
}

// StdinInteractive reports whether stdin is a terminal (exported for callers
// that gate interactive trust prompts).
func StdinInteractive() bool { return isStdinInteractive() }

// Load loads and returns the current configuration.
func Load() (*models.Config, error) {
	var cfg models.Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := expandConfigPaths(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandConfigPaths expands all path fields in the configuration.
func expandConfigPaths(cfg *models.Config) error {
	expandedPath, err := utils.ExpandPath(cfg.Worktree.BaseDir)
	if err != nil {
		return fmt.Errorf("failed to expand worktree base dir: %w", err)
	}
	cfg.Worktree.BaseDir = expandedPath

	for i := range cfg.RepositorySettings {
		repo := cfg.RepositorySettings[i].Repository
		// Skip path expansion for glob patterns — ExpandPath would prepend
		// the CWD to relative globs like "**/owner/repo", breaking matching.
		if !strings.ContainsAny(repo, "*?[") {
			expandedPath, err = utils.ExpandPath(repo)
			if err != nil {
				return fmt.Errorf("failed to expand repository setting path: %w", err)
			}
			// Resolve symlinks for consistent path comparison with git-derived paths
			if resolved, err := filepath.EvalSymlinks(expandedPath); err == nil {
				expandedPath = resolved
			}
			cfg.RepositorySettings[i].Repository = expandedPath
		}

		// BaseDir is a destination path (not compared against git-derived paths),
		// so symlink resolution is not needed here.
		if baseDir := cfg.RepositorySettings[i].BaseDir; baseDir != "" {
			expanded, err := utils.ExpandPath(baseDir)
			if err != nil {
				return fmt.Errorf("failed to expand repository basedir: %w", err)
			}
			cfg.RepositorySettings[i].BaseDir = expanded
		}
	}
	for i := range cfg.Projects {
		path := cfg.Projects[i].Path
		if path == "" {
			continue
		}
		expandedPath, err = utils.ExpandPath(path)
		if err != nil {
			return fmt.Errorf("failed to expand project path: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(expandedPath); err == nil {
			expandedPath = resolved
		}
		cfg.Projects[i].Path = expandedPath
	}
	return nil
}

// RegisterProject records a repository in the global project registry.
func RegisterProject(project models.Project) error {
	if strings.TrimSpace(project.Path) == "" {
		return fmt.Errorf("project path required")
	}

	path, err := normalizeProjectPath(project.Path)
	if err != nil {
		return err
	}
	project.Path = path
	if project.Name == "" {
		project.Name = filepath.Base(path)
	}
	if project.Repository == "" {
		project.Repository = path
	}
	project.LastTouched = time.Now().UTC().Format(time.RFC3339)

	configDir := getConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	globalViper := viper.New()
	globalViper.SetConfigName(configName)
	globalViper.SetConfigType(configType)
	globalViper.AddConfigPath(configDir)
	if err := globalViper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("failed to read config: %w", err)
		}
	}

	var projects []models.Project
	if err := globalViper.UnmarshalKey("projects", &projects); err != nil {
		return fmt.Errorf("failed to read projects: %w", err)
	}

	updated := false
	for i := range projects {
		if sameProject(projects[i], project) {
			projects[i] = project
			updated = true
			break
		}
	}
	if !updated {
		projects = append(projects, project)
	}

	globalViper.Set("projects", projects)
	configPath := filepath.Join(configDir, configName+"."+configType)
	if err := globalViper.WriteConfigAs(configPath); err != nil {
		return err
	}

	viper.Set("projects", projects)
	return nil
}

func normalizeProjectPath(path string) (string, error) {
	expanded, err := utils.ExpandPath(path)
	if err != nil {
		return "", fmt.Errorf("failed to expand project path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(expanded); err == nil {
		expanded = resolved
	}
	return expanded, nil
}

func sameProject(existing, next models.Project) bool {
	if sameProjectPath(existing.Path, next.Path) {
		return true
	}
	if existing.Repository != "" && next.Repository != "" {
		return existing.Repository == next.Repository
	}
	return existing.Path == next.Path
}

func sameProjectPath(existingPath, nextPath string) bool {
	if existingPath == "" || nextPath == "" {
		return false
	}
	existingNormalized, err := normalizeProjectPath(existingPath)
	if err != nil {
		existingNormalized = filepath.Clean(existingPath)
	}
	nextNormalized, err := normalizeProjectPath(nextPath)
	if err != nil {
		nextNormalized = filepath.Clean(nextPath)
	}
	return existingNormalized == nextNormalized
}

// SetGlobal sets a configuration value and writes to the global config file only.
// This uses a separate viper instance to avoid writing merged local settings.
func SetGlobal(key string, value any) error {
	globalViper := viper.New()
	globalViper.SetConfigName(configName)
	globalViper.SetConfigType(configType)
	globalViper.AddConfigPath(getConfigDir())

	// Read only global config (ignore error if file doesn't exist)
	_ = globalViper.ReadInConfig()
	globalViper.Set(key, value)

	configPath := filepath.Join(getConfigDir(), configName+"."+configType)
	if err := globalViper.WriteConfigAs(configPath); err != nil {
		return err
	}

	// Update main viper instance as well
	viper.Set(key, value)
	return nil
}

// SetLocal sets a configuration value and writes to the local config file (.kwt.toml).
func SetLocal(key string, value any) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	localConfigPath := filepath.Join(cwd, localConfigName+"."+configType)

	localViper := viper.New()
	localViper.SetConfigFile(localConfigPath)
	localViper.SetConfigType(configType)

	_ = localViper.ReadInConfig()
	localViper.Set(key, value)

	if err := localViper.WriteConfigAs(localConfigPath); err != nil {
		return fmt.Errorf("failed to write local config: %w", err)
	}

	// Update main viper instance as well
	viper.Set(key, value)
	return nil
}

// Set sets a configuration value (defaults to global).
func Set(key string, value any) error {
	return SetGlobal(key, value)
}

// GetValue retrieves a configuration value by key.
func GetValue(key string) any {
	return viper.Get(key)
}

// AllSettings returns all configuration settings.
func AllSettings() map[string]any {
	return viper.AllSettings()
}

// Get returns the current loaded configuration, loading it if necessary.
func Get() *models.Config {
	cfg, err := Load()
	if err != nil {
		// Initialize with viper defaults if config cannot be loaded
		var defaultCfg models.Config
		if err := viper.Unmarshal(&defaultCfg); err != nil {
			return &models.Config{}
		}

		// Apply path expansions to defaults, ignoring errors
		_ = expandConfigPaths(&defaultCfg)
		return &defaultCfg
	}
	return cfg
}
