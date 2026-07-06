# Directory Workspaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let kwt register plain directories as tmux workspaces, open them with layouts, and surface them in the TUI dashboard.

**Architecture:** A `[[workspaces]]` registry in global config, a `kwt workspace` CLI group, a third `dashboard.Row` flavor (`Workspace`), and a `kwt-workspace-dir-{name}-{hash8(path)}` tmux session namespace. Liveness and attach match sessions by the trailing path hash so renames re-attach instead of orphaning.

**Tech Stack:** Go, cobra, viper, tmux, testify. Spec: `docs/superpowers/specs/2026-07-06-directory-workspaces-design.md`.

## Global Constraints

- Work in worktree `/Users/wesm/.config/superpowers/worktrees/kwt/feature-directory-workspaces` on branch `feature/directory-workspaces`.
- Run `go test ./<package>` after each task; `golangci-lint run` must report 0 issues before each commit (pre-push hook enforces `make lint`, `golangci-lint fmt -d`, `oxfmt --check` on markdown).
- `workspaces` is machine-level config: excluded from repo-local `.kwt.toml` merge, never published in fleet manifests.
- Never auto-register the user's home directory.
- Errors: fail fast with the offending value (except never echo credentials — not applicable here). Unknown `remove` names list registered names.
- Commit messages: imperative mood, ≤72-char subject, no `--amend`, end body with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

### Task 1: models.Workspace and config plumbing

**Files:**

- Modify: `pkg/models/models.go` (add `Workspace` type; add `Workspaces` field to `Config` next to `Projects`)
- Modify: `internal/config/config.go` (`expandConfigPaths`, `mergeLocalConfig` skip list)
- Test: `internal/config/config_test.go`

**Interfaces:**

- Produces: `models.Workspace{Name string; Path string}`; `models.Config.Workspaces []models.Workspace`. Later tasks read `cfg.Workspaces` with expanded, symlink-resolved paths.

- [ ] **Step 1: Write failing tests** — append to `internal/config/config_test.go`:

```go
func TestLoadExpandsWorkspacePaths(t *testing.T) {
	viper.Reset()
	t.Cleanup(func() { viper.Reset() })

	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "notes")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	viper.SetConfigType("toml")
	viper.Set("workspaces", []map[string]any{{"name": "notes", "path": "~/notes"}})

	cfg, err := Load()

	require.NoError(t, err)
	require.Len(t, cfg.Workspaces, 1)
	assert.Equal(t, "notes", cfg.Workspaces[0].Name)
	assert.Equal(t, dir, cfg.Workspaces[0].Path)
}
```

Add a subtest to `TestMergeLocalConfig` (same file, mirrors `IgnoresFleetSettings`):

```go
	t.Run("IgnoresWorkspaces", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(func() { viper.Reset() })

		tmpDir := t.TempDir()
		localConfig := []byte(`
[[workspaces]]
name = "evil"
path = "/tmp/evil"
`)
		if err := os.WriteFile(filepath.Join(tmpDir, ".kwt.toml"), localConfig, 0644); err != nil {
			t.Fatalf("Failed to create local config: %v", err)
		}
		changeDir(t, tmpDir)

		viper.SetConfigType("toml")

		if err := mergeLocalConfig(&TrustStore{}, trustingPrompter(), true); err != nil {
			t.Fatalf("mergeLocalConfig() error = %v", err)
		}
		if viper.IsSet("workspaces") {
			t.Errorf("workspaces must not be settable from local config")
		}
	})
```

Note: `TestLoadExpandsWorkspacePaths` needs `require`/`assert` imports; the file already imports them if other tests use them — check the import block and add `"github.com/stretchr/testify/assert"` / `"github.com/stretchr/testify/require"` if absent.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run "TestLoadExpandsWorkspacePaths|TestMergeLocalConfig/IgnoresWorkspaces" -v`
Expected: FAIL (`cfg.Workspaces` undefined → compile error is the failure mode for the first test).

- [ ] **Step 3: Implement** — in `pkg/models/models.go`, after the `Project` struct:

```go
// Workspace records a plain directory kwt can open as a tmux workspace,
// independent of any git worktree.
type Workspace struct {
	Name string `mapstructure:"name" toml:"name"` // Unique display name
	Path string `mapstructure:"path" toml:"path"` // Directory root
}
```

In the `Config` struct, after the `Projects` field:

```go
	Workspaces         []Workspace         `mapstructure:"workspaces" toml:"workspaces"` // Registered directory workspaces
```

In `internal/config/config.go` `expandConfigPaths`, after the `cfg.Projects` loop:

```go
	for i := range cfg.Workspaces {
		path := cfg.Workspaces[i].Path
		if path == "" {
			continue
		}
		expandedPath, err = utils.ExpandPath(path)
		if err != nil {
			return fmt.Errorf("failed to expand workspace path: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(expandedPath); err == nil {
			expandedPath = resolved
		}
		cfg.Workspaces[i].Path = expandedPath
	}
```

In `mergeLocalConfig`'s key switch, extend the `projects` case:

```go
		case key == "projects" || key == "workspaces":
			continue
```

(Replace the existing `case key == "projects":` line; keep the comment style of the surrounding code.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v -run "TestLoadExpandsWorkspacePaths|TestMergeLocalConfig"`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add pkg/models/models.go internal/config/config.go internal/config/config_test.go
git commit -m "Add workspace registry to config model"
```

---

### Task 2: RegisterWorkspace and UnregisterWorkspace

**Files:**

- Create: `internal/config/workspace.go`
- Test: `internal/config/workspace_test.go`

**Interfaces:**

- Consumes: `models.Workspace` from Task 1; existing unexported helpers `getConfigDir`, `configName`, `configType`, `normalizeProjectPath` in package `config`.
- Produces: `config.RegisterWorkspace(workspace models.Workspace) (models.Workspace, error)` (returns the normalized entry actually stored) and `config.UnregisterWorkspace(name string) error`. Errors: missing/non-dir path → `workspace path <path> is not a directory`; duplicate name for different path → `workspace name %q is already registered for <path>; choose another with --name`; unregister unknown name → `no workspace named %q; registered: a, b, c` (or `no workspaces registered`).

- [ ] **Step 1: Write failing tests** — create `internal/config/workspace_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/pkg/models"
)

func workspaceTestEnv(t *testing.T) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(func() { viper.Reset() })
	configHome := t.TempDir()
	t.Setenv("KWT_HOME", configHome)
	return configHome
}

func registeredWorkspaces(t *testing.T) []models.Workspace {
	t.Helper()
	globalViper := viper.New()
	globalViper.SetConfigFile(filepath.Join(getConfigDir(), configName+"."+configType))
	globalViper.SetConfigType(configType)
	require.NoError(t, globalViper.ReadInConfig())
	var workspaces []models.Workspace
	require.NoError(t, globalViper.UnmarshalKey("workspaces", &workspaces))
	return workspaces
}

func TestRegisterWorkspaceDefaultsNameAndPersists(t *testing.T) {
	workspaceTestEnv(t)
	dir := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	stored, err := RegisterWorkspace(models.Workspace{Path: dir})

	require.NoError(t, err)
	assert.Equal(t, "notes", stored.Name)
	workspaces := registeredWorkspaces(t)
	require.Len(t, workspaces, 1)
	assert.Equal(t, "notes", workspaces[0].Name)
}

func TestRegisterWorkspaceRejectsMissingPath(t *testing.T) {
	workspaceTestEnv(t)

	_, err := RegisterWorkspace(models.Workspace{Path: filepath.Join(t.TempDir(), "absent")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestRegisterWorkspaceUpdatesNameForSamePath(t *testing.T) {
	workspaceTestEnv(t)
	dir := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := RegisterWorkspace(models.Workspace{Name: "old", Path: dir})
	require.NoError(t, err)

	_, err = RegisterWorkspace(models.Workspace{Name: "new", Path: dir})

	require.NoError(t, err)
	workspaces := registeredWorkspaces(t)
	require.Len(t, workspaces, 1)
	assert.Equal(t, "new", workspaces[0].Name)
}

func TestRegisterWorkspaceRejectsDuplicateNameForDifferentPath(t *testing.T) {
	workspaceTestEnv(t)
	base := t.TempDir()
	first := filepath.Join(base, "a")
	second := filepath.Join(base, "b")
	require.NoError(t, os.MkdirAll(first, 0o755))
	require.NoError(t, os.MkdirAll(second, 0o755))
	_, err := RegisterWorkspace(models.Workspace{Name: "notes", Path: first})
	require.NoError(t, err)

	_, err = RegisterWorkspace(models.Workspace{Name: "notes", Path: second})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--name")
}

func TestUnregisterWorkspace(t *testing.T) {
	workspaceTestEnv(t)
	dir := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	_, err := RegisterWorkspace(models.Workspace{Path: dir})
	require.NoError(t, err)

	require.NoError(t, UnregisterWorkspace("notes"))
	assert.Empty(t, registeredWorkspaces(t))

	err = UnregisterWorkspace("notes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run "TestRegisterWorkspace|TestUnregisterWorkspace" -v`
Expected: FAIL to compile (`RegisterWorkspace` undefined).

- [ ] **Step 3: Implement** — create `internal/config/workspace.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/viper"
	"go.kenn.io/kwt/pkg/models"
)

// RegisterWorkspace records a directory workspace in the global config.
// The path is expanded and resolved; an empty name defaults to the directory
// base name. Re-registering an existing path updates its name; registering an
// existing name for a different path is an error.
func RegisterWorkspace(workspace models.Workspace) (models.Workspace, error) {
	path, err := normalizeProjectPath(workspace.Path)
	if err != nil {
		return models.Workspace{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return models.Workspace{}, fmt.Errorf("workspace path %s is not a directory", workspace.Path)
	}
	workspace.Path = path
	if strings.TrimSpace(workspace.Name) == "" {
		workspace.Name = filepath.Base(path)
	}
	workspace.Name = strings.TrimSpace(workspace.Name)

	workspaces, globalViper, err := readGlobalWorkspaces()
	if err != nil {
		return models.Workspace{}, err
	}

	updated := false
	for i := range workspaces {
		if workspaces[i].Path == workspace.Path {
			workspaces[i] = workspace
			updated = true
			continue
		}
		if strings.EqualFold(workspaces[i].Name, workspace.Name) {
			return models.Workspace{}, fmt.Errorf(
				"workspace name %q is already registered for %s; choose another with --name",
				workspace.Name, workspaces[i].Path,
			)
		}
	}
	if !updated {
		workspaces = append(workspaces, workspace)
	}
	if err := writeGlobalWorkspaces(globalViper, workspaces); err != nil {
		return models.Workspace{}, err
	}
	return workspace, nil
}

// UnregisterWorkspace removes a directory workspace from the global config by
// name. The directory itself is never touched.
func UnregisterWorkspace(name string) error {
	name = strings.TrimSpace(name)
	workspaces, globalViper, err := readGlobalWorkspaces()
	if err != nil {
		return err
	}

	kept := workspaces[:0]
	removed := false
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.Name, name) {
			removed = true
			continue
		}
		kept = append(kept, workspace)
	}
	if !removed {
		if len(workspaces) == 0 {
			return fmt.Errorf("no workspace named %q; no workspaces registered", name)
		}
		names := make([]string, 0, len(workspaces))
		for _, workspace := range workspaces {
			names = append(names, workspace.Name)
		}
		sort.Strings(names)
		return fmt.Errorf("no workspace named %q; registered: %s", name, strings.Join(names, ", "))
	}
	return writeGlobalWorkspaces(globalViper, kept)
}

func readGlobalWorkspaces() ([]models.Workspace, *viper.Viper, error) {
	configDir := getConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create config directory: %w", err)
	}
	globalViper := viper.New()
	globalViper.SetConfigName(configName)
	globalViper.SetConfigType(configType)
	globalViper.AddConfigPath(configDir)
	if err := globalViper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, nil, fmt.Errorf("failed to read config: %w", err)
		}
	}
	var workspaces []models.Workspace
	if err := globalViper.UnmarshalKey("workspaces", &workspaces); err != nil {
		return nil, nil, fmt.Errorf("failed to read workspaces: %w", err)
	}
	return workspaces, globalViper, nil
}

func writeGlobalWorkspaces(globalViper *viper.Viper, workspaces []models.Workspace) error {
	globalViper.Set("workspaces", workspaces)
	configPath := filepath.Join(getConfigDir(), configName+"."+configType)
	if err := globalViper.WriteConfigAs(configPath); err != nil {
		return err
	}
	viper.Set("workspaces", workspaces)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -run "TestRegisterWorkspace|TestUnregisterWorkspace" -v`
Expected: PASS (5 tests). Also run `go test ./internal/config/` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/config/workspace.go internal/config/workspace_test.go
git commit -m "Add workspace register and unregister helpers"
```

---

### Task 3: Directory workspace session naming and hash matching

**Files:**

- Modify: `internal/tmux/session.go`
- Test: `internal/tmux/session_test.go` (append; create if the file does not exist)

**Interfaces:**

- Consumes: `template.ShortHash(input string) string` (8-hex-char hash), unexported `sanitizeTmuxName`.
- Produces: `tmux.DirWorkspaceSessionName(name, path string) string` returning `kwt-workspace-dir-{sanitized name}-{hash8(path)}`; `tmux.MatchDirWorkspaceSession(sessions []string, path string) (string, bool)` returning the live session whose name has prefix `kwt-workspace-dir-` and suffix `-{hash8(path)}`.

- [ ] **Step 1: Write failing tests** — append to `internal/tmux/session_test.go`:

```go
func TestDirWorkspaceSessionName(t *testing.T) {
	name := DirWorkspaceSessionName("my notes", "/Users/me/notes")

	assert.True(t, strings.HasPrefix(name, "kwt-workspace-dir-my-notes-"), name)
	assert.Regexp(t, `^kwt-workspace-dir-my-notes-[0-9a-f]{8}$`, name)
	assert.Equal(t, name, DirWorkspaceSessionName("my notes", "/Users/me/notes"),
		"session names must be deterministic")
	assert.NotEqual(t, name, DirWorkspaceSessionName("my notes", "/Users/me/other"),
		"different paths must not collide")
}

func TestMatchDirWorkspaceSessionMatchesByPathHash(t *testing.T) {
	current := DirWorkspaceSessionName("new-name", "/Users/me/notes")
	old := DirWorkspaceSessionName("old-name", "/Users/me/notes")
	sessions := []string{"kwt-workspace-foo-12345678", old, "unrelated"}

	got, ok := MatchDirWorkspaceSession(sessions, "/Users/me/notes")

	require.True(t, ok)
	assert.Equal(t, old, got, "renamed workspaces must re-attach to the live old-name session")
	assert.NotEqual(t, current, got)

	_, ok = MatchDirWorkspaceSession([]string{"kwt-workspace-foo-12345678"}, "/Users/me/notes")
	assert.False(t, ok)
}
```

(Check the file's imports: needs `strings`, `testing`, testify `assert`/`require`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tmux/ -run "TestDirWorkspaceSessionName|TestMatchDirWorkspaceSession" -v`
Expected: FAIL to compile (`DirWorkspaceSessionName` undefined).

- [ ] **Step 3: Implement** — append to `internal/tmux/session.go`:

```go
const dirWorkspaceSessionPrefix = "kwt-workspace-dir-"

// DirWorkspaceSessionName returns a stable, tmux-safe session name for a
// directory workspace: kwt-workspace-dir-{name}-{hash}. The hash is computed
// over the workspace path so renames keep a recognizable suffix and distinct
// directories never collide.
func DirWorkspaceSessionName(name, path string) string {
	raw := fmt.Sprintf("%s%s-%s", dirWorkspaceSessionPrefix, name, template.ShortHash(path))
	return sanitizeTmuxName(raw)
}

// MatchDirWorkspaceSession finds the live directory-workspace session for
// path, matching by prefix and trailing path hash rather than the full name
// so a renamed workspace still finds its running session.
func MatchDirWorkspaceSession(sessions []string, path string) (string, bool) {
	suffix := "-" + template.ShortHash(path)
	for _, session := range sessions {
		if strings.HasPrefix(session, dirWorkspaceSessionPrefix) && strings.HasSuffix(session, suffix) {
			return session, true
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tmux/ -v -run "TestDirWorkspaceSessionName|TestMatchDirWorkspaceSession"`
Expected: PASS. Also `go test ./internal/tmux/` for regressions (the existing `workspaceSessionRe` parsing test must still pass — `kwt-workspace-dir-...-{hash8}` matches it).

- [ ] **Step 5: Commit**

```bash
git add internal/tmux/session.go internal/tmux/session_test.go
git commit -m "Add directory workspace session naming and matching"
```

---

### Task 4: kwt workspace CLI (add, list, remove)

**Files:**

- Create: `internal/cmd/workspace.go`
- Test: `internal/cmd/workspace_test.go`

**Interfaces:**

- Consumes: `config.RegisterWorkspace`, `config.UnregisterWorkspace` (Task 2); `tmux.DirWorkspaceSessionName`, `tmux.MatchDirWorkspaceSession` (Task 3); `config.Load`; `tmux.NewTmuxCommand("").ListSessions()`.
- Produces: `kwt workspace add [path] [--name X]`, `kwt workspace list`, `kwt workspace remove <name>`. Package-level function vars `registerWorkspace`, `unregisterWorkspace`, `loadWorkspaceConfig`, `listWorkspaceSessions` for test stubbing (follow the `loadFleetConfig` pattern in `internal/cmd/fleet.go`).

- [ ] **Step 1: Write failing tests** — create `internal/cmd/workspace_test.go`:

```go
package cmd

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/tmux"
	"go.kenn.io/kwt/pkg/models"
)

func changeDir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func resetWorkspaceCommandDeps(t *testing.T) {
	t.Helper()
	origRegister := registerWorkspace
	origUnregister := unregisterWorkspace
	origLoad := loadWorkspaceConfig
	origSessions := listWorkspaceSessions
	t.Cleanup(func() {
		registerWorkspace = origRegister
		unregisterWorkspace = origUnregister
		loadWorkspaceConfig = origLoad
		listWorkspaceSessions = origSessions
	})
}

func TestWorkspaceAddRegistersCwdByDefault(t *testing.T) {
	resetWorkspaceCommandDeps(t)
	dir := t.TempDir()
	changeDir(t, dir)
	var got models.Workspace
	registerWorkspace = func(workspace models.Workspace) (models.Workspace, error) {
		got = workspace
		workspace.Name = "resolved"
		return workspace, nil
	}

	cmd, stdout, _ := fleetTestCommand()
	err := runWorkspaceAdd(cmd, nil)

	require.NoError(t, err)
	assert.Equal(t, dir, got.Path)
	assert.Empty(t, got.Name)
	assert.Contains(t, stdout.String(), "resolved")
}

func TestWorkspaceAddUsesArgsAndNameFlag(t *testing.T) {
	resetWorkspaceCommandDeps(t)
	var got models.Workspace
	registerWorkspace = func(workspace models.Workspace) (models.Workspace, error) {
		got = workspace
		return workspace, nil
	}

	cmd, _, _ := fleetTestCommand()
	workspaceAddName = "scratch"
	t.Cleanup(func() { workspaceAddName = "" })
	err := runWorkspaceAdd(cmd, []string{"/tmp/somewhere"})

	require.NoError(t, err)
	assert.Equal(t, "/tmp/somewhere", got.Path)
	assert.Equal(t, "scratch", got.Name)
}

func TestWorkspaceListShowsLiveState(t *testing.T) {
	resetWorkspaceCommandDeps(t)
	loadWorkspaceConfig = func() (*models.Config, error) {
		return &models.Config{Workspaces: []models.Workspace{
			{Name: "notes", Path: "/Users/me/notes"},
			{Name: "scratch", Path: "/Users/me/scratch"},
		}}, nil
	}
	listWorkspaceSessions = func() ([]string, error) {
		return []string{tmuxDirSessionNameForTest("notes", "/Users/me/notes")}, nil
	}

	cmd, stdout, _ := fleetTestCommand()
	err := runWorkspaceList(cmd, nil)

	require.NoError(t, err)
	out := stdout.String()
	assert.Contains(t, out, "notes")
	assert.Contains(t, out, "live")
	assert.Contains(t, out, "scratch")
	assert.Contains(t, out, "stopped")
}

func TestWorkspaceRemoveReportsLiveSession(t *testing.T) {
	resetWorkspaceCommandDeps(t)
	loadWorkspaceConfig = func() (*models.Config, error) {
		return &models.Config{Workspaces: []models.Workspace{{Name: "notes", Path: "/Users/me/notes"}}}, nil
	}
	listWorkspaceSessions = func() ([]string, error) {
		return []string{tmuxDirSessionNameForTest("notes", "/Users/me/notes")}, nil
	}
	unregisterWorkspace = func(name string) error {
		assert.Equal(t, "notes", name)
		return nil
	}

	cmd, stdout, _ := fleetTestCommand()
	err := runWorkspaceRemove(cmd, []string{"notes"})

	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "still running")
}

func TestWorkspaceRemovePropagatesUnknownNameError(t *testing.T) {
	resetWorkspaceCommandDeps(t)
	loadWorkspaceConfig = func() (*models.Config, error) { return &models.Config{}, nil }
	listWorkspaceSessions = func() ([]string, error) { return nil, nil }
	unregisterWorkspace = func(name string) error {
		return errors.New(`no workspace named "nope"; no workspaces registered`)
	}

	cmd, _, _ := fleetTestCommand()
	err := runWorkspaceRemove(cmd, []string{"nope"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace named")
}
```

Add this helper at the bottom:

```go
func tmuxDirSessionNameForTest(name, path string) string {
	return tmux.DirWorkspaceSessionName(name, path)
}
```

(`fleetTestCommand` already exists in `internal/cmd/fleet_test.go` — reuse it, do not redefine. `changeDir` exists only in `internal/config`, not this package, hence the copy above.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cmd/ -run TestWorkspace -v`
Expected: FAIL to compile (`runWorkspaceAdd` undefined).

- [ ] **Step 3: Implement** — create `internal/cmd/workspace.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.kenn.io/kwt/internal/config"
	"go.kenn.io/kwt/internal/table"
	"go.kenn.io/kwt/internal/tmux"
	"go.kenn.io/kwt/pkg/models"
)

var (
	workspaceAddName string

	registerWorkspace     = config.RegisterWorkspace
	unregisterWorkspace   = config.UnregisterWorkspace
	loadWorkspaceConfig   = config.Load
	listWorkspaceSessions = func() ([]string, error) {
		return tmux.NewTmuxCommand("").ListSessions()
	}
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage directory workspaces not bound to a git worktree",
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Register a directory as a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkspaceAdd,
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered directory workspaces",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceList,
}

var workspaceRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Unregister a directory workspace (never deletes the directory)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceRemove,
}

func init() {
	workspaceAddCmd.Flags().StringVar(&workspaceAddName, "name", "", "workspace name (defaults to the directory base name)")
	workspaceCmd.AddCommand(workspaceAddCmd, workspaceListCmd, workspaceRemoveCmd)
	rootCmd.AddCommand(workspaceCmd)
}

func runWorkspaceAdd(cmd *cobra.Command, args []string) error {
	path := ""
	if len(args) == 1 {
		path = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to resolve current directory: %w", err)
		}
		path = cwd
	}
	stored, err := registerWorkspace(models.Workspace{Name: workspaceAddName, Path: path})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "registered workspace %s at %s\n", stored.Name, stored.Path)
	return nil
}

func runWorkspaceList(cmd *cobra.Command, args []string) error {
	cfg, err := loadWorkspaceConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if len(cfg.Workspaces) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no workspaces registered")
		return nil
	}
	sessions, err := listWorkspaceSessions()
	if err != nil {
		sessions = nil
	}
	t := table.New().SetOutput(cmd.OutOrStdout()).Headers("NAME", "PATH", "SESSION")
	for _, workspace := range cfg.Workspaces {
		state := "stopped"
		if _, ok := tmux.MatchDirWorkspaceSession(sessions, workspace.Path); ok {
			state = "live"
		}
		t.Row(workspace.Name, workspace.Path, state)
	}
	return t.Println()
}

func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, cfgErr := loadWorkspaceConfig()
	livePath := ""
	if cfgErr == nil {
		for _, workspace := range cfg.Workspaces {
			if workspace.Name == name {
				livePath = workspace.Path
			}
		}
	}
	if err := unregisterWorkspace(name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unregistered workspace %s\n", name)
	if livePath != "" {
		sessions, err := listWorkspaceSessions()
		if err == nil {
			if session, ok := tmux.MatchDirWorkspaceSession(sessions, livePath); ok {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"its tmux session %s is still running; kill it with: tmux kill-session -t %s\n",
					session, session)
			}
		}
	}
	return nil
}
```

(The `table.New().SetOutput(w).Headers(...)` / `t.Row(...)` / `t.Println()` chain matches `internal/fleet/render.go` `RenderStatusTable` — same builder.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cmd/ -run TestWorkspace -v`
Expected: PASS (5 tests). Then `go build ./...` and `kwt workspace --help` via `go run ./cmd/kwt workspace --help` to eyeball command wiring.

- [ ] **Step 5: Commit**

```bash
git add internal/cmd/workspace.go internal/cmd/workspace_test.go
git commit -m "Add kwt workspace add, list, and remove commands"
```

---

### Task 5: Workspace rows in the TUI backend + auto-registration

**Files:**

- Modify: `internal/tui/backend.go` (add `WorkspaceInfo`, `Row.Workspace` field)
- Modify: `internal/cmd/tui_backend.go` (`List` appends workspace rows; auto-register non-git launch dir; `registerWorkspace` hook field on `tuiBackend`)
- Test: `internal/cmd/tui_test.go`

**Interfaces:**

- Consumes: `cfg.Workspaces` (Task 1), `config.RegisterWorkspace` (Task 2), `tmux.DirWorkspaceSessionName`/`MatchDirWorkspaceSession` (Task 3).
- Produces: `dashboard.WorkspaceInfo{Name, Path string}`; `Row.Workspace *WorkspaceInfo` (mutually exclusive with `Entry` and `Fleet`); `tuiBackend.registerWorkspace func(models.Workspace) (models.Workspace, error)` hook. Workspace rows carry `SessionName` (live session name if matched by hash, else `DirWorkspaceSessionName`) and `SessionLive`.

- [ ] **Step 1: Write failing tests** — append to `internal/cmd/tui_test.go`:

```go
func TestTUIBackendListIncludesWorkspaceRows(t *testing.T) {
	dir := t.TempDir()
	cfg := &models.Config{
		Worktree:   models.WorktreeConfig{BaseDir: "/global"},
		Workspaces: []models.Workspace{{Name: "notes", Path: dir}},
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverProjectWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverLaunchWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return nil, nil
	}
	liveSession := tmux.DirWorkspaceSessionName("old-name", dir)
	backend.listSessions = func() ([]string, error) { return []string{liveSession}, nil }

	rows, _, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Workspace)
	assert.Equal(t, "notes", rows[0].Workspace.Name)
	assert.Equal(t, dir, rows[0].Workspace.Path)
	assert.True(t, rows[0].SessionLive,
		"liveness must match by path hash even under an old session name")
	assert.Equal(t, liveSession, rows[0].SessionName,
		"attach must target the live session, not the freshly computed name")
	assert.Nil(t, rows[0].Entry)
	assert.Nil(t, rows[0].Fleet)
}

func TestTUIBackendAutoRegistersNonGitLaunchDir(t *testing.T) {
	launchDir := t.TempDir()
	cfg := &models.Config{Worktree: models.WorktreeConfig{BaseDir: "/global"}}
	backend := newTUIBackendWithLaunchDir(cfg, launchDir)
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverProjectWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverLaunchWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return nil, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }
	var registered []models.Workspace
	backend.registerWorkspace = func(workspace models.Workspace) (models.Workspace, error) {
		registered = append(registered, workspace)
		return workspace, nil
	}

	_, _, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, registered, 1)
	assert.Equal(t, launchDir, registered[0].Path)
}

func TestTUIBackendNeverAutoRegistersHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &models.Config{Worktree: models.WorktreeConfig{BaseDir: "/global"}}
	backend := newTUIBackendWithLaunchDir(cfg, home)
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverProjectWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.discoverLaunchWorktrees = func(string) ([]*discovery.GlobalWorktreeEntry, error) { return nil, nil }
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return nil, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }
	backend.registerWorkspace = func(workspace models.Workspace) (models.Workspace, error) {
		t.Fatalf("home directory must never be auto-registered, got %v", workspace)
		return workspace, nil
	}

	_, _, err := backend.List(context.Background())

	require.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cmd/ -run "TestTUIBackendListIncludesWorkspaceRows|TestTUIBackendAutoRegisters|TestTUIBackendNeverAutoRegisters" -v`
Expected: FAIL to compile (`rows[0].Workspace`, `backend.registerWorkspace` undefined).

- [ ] **Step 3: Implement.** In `internal/tui/backend.go`, add to `Row` after `Fleet`:

```go
	Workspace   *WorkspaceInfo
```

and below `FleetInfo`:

```go
// WorkspaceInfo is the TUI-facing view of one registered directory workspace.
type WorkspaceInfo struct {
	Name string
	Path string
}
```

In `internal/cmd/tui_backend.go`:

1. Add a field to `tuiBackend`: `registerWorkspace func(models.Workspace) (models.Workspace, error)` and set it in `newTUIBackendWithLaunchDir`: `registerWorkspace: config.RegisterWorkspace,`.
2. In `List`, after `b.registerLaunchProject(launchEntries)` add `b.registerLaunchWorkspace(launchEntries)`, and after the worktree-row loop (before `mergeFleetRows`) append workspace rows:

```go
	rows = append(rows, b.workspaceRows(sessions)...)
```

Note: `List` currently discards the raw `sessions` slice after building `liveSessions`; keep the `sessions` variable in scope for this call.

3. Add the two methods:

```go
func (b *tuiBackend) workspaceRows(sessions []string) []dashboard.Row {
	if b.cfg == nil || len(b.cfg.Workspaces) == 0 {
		return nil
	}
	rows := make([]dashboard.Row, 0, len(b.cfg.Workspaces))
	for _, workspace := range b.cfg.Workspaces {
		sessionName := tmux.DirWorkspaceSessionName(workspace.Name, workspace.Path)
		sessionLive := false
		// Match by path hash so a renamed workspace still finds (and later
		// attaches to) its live session created under the old name.
		if live, ok := tmux.MatchDirWorkspaceSession(sessions, workspace.Path); ok {
			sessionName = live
			sessionLive = true
		}
		rows = append(rows, dashboard.Row{
			Workspace:   &dashboard.WorkspaceInfo{Name: workspace.Name, Path: workspace.Path},
			SessionName: sessionName,
			SessionLive: sessionLive,
		})
	}
	return rows
}

// registerLaunchWorkspace records a non-git launch directory as a workspace,
// best-effort, mirroring launch-repo project registration. The home directory
// is never auto-registered: running kwt from ~ is common and would create a
// junk entry.
func (b *tuiBackend) registerLaunchWorkspace(launchEntries []*discovery.GlobalWorktreeEntry) {
	if b.registerWorkspace == nil || b.launchDir == "" || len(launchEntries) > 0 {
		return
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(home, b.launchDir) {
		return
	}
	stored, err := b.registerWorkspace(models.Workspace{Path: b.launchDir})
	if err != nil {
		return
	}
	b.cfg.Workspaces = upsertWorkspace(b.cfg.Workspaces, stored)
}

func upsertWorkspace(workspaces []models.Workspace, workspace models.Workspace) []models.Workspace {
	for i := range workspaces {
		if samePath(workspaces[i].Path, workspace.Path) {
			workspaces[i] = workspace
			return workspaces
		}
	}
	return append(workspaces, workspace)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cmd/ -run "TestTUIBackend" -v`
Expected: new tests PASS; all pre-existing `TestTUIBackend*` tests still PASS. They stub `registerProject` but not `registerWorkspace`, so update the existing helper at `internal/cmd/tui_test.go:1824`:

```go
func stubTUIProjectRegistration(backend *tuiBackend) {
	backend.registerProject = func(models.Project) error { return nil }
	backend.registerWorkspace = func(workspace models.Workspace) (models.Workspace, error) {
		return workspace, nil
	}
}
```

(Existing tests pass launch dirs like `/repos/other` with non-empty launch entries or empty launch dirs, so none of them hit auto-registration; the stub is defense against nil-func panics only.)

- [ ] **Step 5: Commit**

```bash
git add internal/tui/backend.go internal/cmd/tui_backend.go internal/cmd/tui_test.go
git commit -m "Surface directory workspaces as TUI rows"
```

---

### Task 6: Backend actions for workspace rows

**Files:**

- Modify: `internal/cmd/tui_backend.go` (`attachWorkspace`, `sessionName`, `resolveLayout`), `internal/cmd/tui.go` (`rowPathForHandoff`)
- Modify: `internal/tui/backend.go` (add `UnregisterWorkspace(row Row) error` to the `Backend` interface)
- Modify: `internal/cmd/tui_backend.go` (implement `UnregisterWorkspace`; add `unregisterWorkspace` hook field)
- Test: `internal/cmd/tui_test.go`

**Interfaces:**

- Consumes: `Row.Workspace` (Task 5), `config.UnregisterWorkspace` (Task 2), `config.LoadRepoLayoutDefault(dir, interactive)` (existing).
- Produces: workspace rows are openable/attachable/killable through the existing `OpenInTmux`/`AttachOutsideTmux`/`KillSession`; `Backend.UnregisterWorkspace(row Row) error`; `rowPathForHandoff` returns `Workspace.Path` for workspace rows.

- [ ] **Step 1: Write failing tests** — append to `internal/cmd/tui_test.go`:

```go
func TestTUIBackendSessionNameAndHandoffPathForWorkspaceRow(t *testing.T) {
	row := dashboard.Row{
		Workspace:   &dashboard.WorkspaceInfo{Name: "notes", Path: "/Users/me/notes"},
		SessionName: tmux.DirWorkspaceSessionName("notes", "/Users/me/notes"),
	}
	backend := newTUIBackendWithLaunchDir(&models.Config{}, "")

	name, err := backend.sessionName(row)

	require.NoError(t, err)
	assert.Equal(t, row.SessionName, name)
	assert.Equal(t, "/Users/me/notes", rowPathForHandoff(row))
}

func TestTUIBackendUnregisterWorkspace(t *testing.T) {
	cfg := &models.Config{Workspaces: []models.Workspace{{Name: "notes", Path: "/Users/me/notes"}}}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	var removed []string
	backend.unregisterWorkspace = func(name string) error {
		removed = append(removed, name)
		return nil
	}

	err := backend.UnregisterWorkspace(dashboard.Row{
		Workspace: &dashboard.WorkspaceInfo{Name: "notes", Path: "/Users/me/notes"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"notes"}, removed)
	assert.Empty(t, cfg.Workspaces, "unregister must also drop the in-memory entry")

	err = backend.UnregisterWorkspace(dashboard.Row{})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cmd/ -run "TestTUIBackendSessionNameAndHandoffPathForWorkspaceRow|TestTUIBackendUnregisterWorkspace" -v`
Expected: FAIL to compile (`backend.unregisterWorkspace`, `UnregisterWorkspace` undefined). Note `sessionName` currently errors on nil `Entry`, so the first test fails even once it compiles.

- [ ] **Step 3: Implement.**

In `internal/cmd/tui.go` `rowPathForHandoff`, add before the `Entry` check:

```go
	if row.Workspace != nil {
		return row.Workspace.Path
	}
```

In `internal/cmd/tui_backend.go`:

1. `sessionName`: add before the nil-Entry error:

```go
	if row.Workspace != nil {
		return tmux.DirWorkspaceSessionName(row.Workspace.Name, row.Workspace.Path), nil
	}
```

(the `row.SessionName != ""` fast path above already returns the hash-matched live name when the row came from `List`).

2. `attachWorkspace`: replace the leading guard

```go
	if row.Entry == nil {
		return fmt.Errorf("no worktree selected")
	}
```

with

```go
	if row.Entry == nil && row.Workspace == nil {
		return fmt.Errorf("no worktree selected")
	}
```

and replace `row.Entry.Path` in the `EnsureAndAttach` call with `rowPaneRoot(row)`; add:

```go
func rowPaneRoot(row dashboard.Row) string {
	if row.Workspace != nil {
		return row.Workspace.Path
	}
	if row.Entry != nil {
		return row.Entry.Path
	}
	return ""
}
```

3. `resolveLayout`: in the `layoutName == ""` branch, resolve the default-layout directory from the workspace when present:

```go
		var layoutRoot string
		if row.Workspace != nil {
			layoutRoot = row.Workspace.Path
		} else {
			layoutRoot, err = b.repositoryRootForRow(row)
			if err != nil {
				return models.Layout{}, err
			}
		}
		var targetDefault string
		targetDefault, err = config.LoadRepoLayoutDefault(layoutRoot, interactive)
```

(rename the existing `repoRoot` variable accordingly).

4. Add the hook field to `tuiBackend`: `unregisterWorkspace func(name string) error`, defaulted in `newTUIBackendWithLaunchDir` to `config.UnregisterWorkspace`. Implement the interface method:

```go
func (b *tuiBackend) UnregisterWorkspace(row dashboard.Row) error {
	if row.Workspace == nil {
		return fmt.Errorf("no workspace selected")
	}
	if err := b.unregisterWorkspace(row.Workspace.Name); err != nil {
		return err
	}
	if b.cfg != nil {
		kept := b.cfg.Workspaces[:0]
		for _, workspace := range b.cfg.Workspaces {
			if !samePath(workspace.Path, row.Workspace.Path) {
				kept = append(kept, workspace)
			}
		}
		b.cfg.Workspaces = kept
	}
	return nil
}
```

5. In `internal/tui/backend.go`, add to the `Backend` interface after `RemoveWorktree`:

```go
	UnregisterWorkspace(row Row) error
```

6. Update `fakeBackend` in `internal/tui/model_test.go`:

```go
func (b *fakeBackend) UnregisterWorkspace(row Row) error {
	b.unregistered = append(b.unregistered, row)
	return nil
}
```

and add `unregistered []Row` to the `fakeBackend` struct.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cmd/ ./internal/tui/ -v -run "TestTUIBackendSessionNameAndHandoffPathForWorkspaceRow|TestTUIBackendUnregisterWorkspace"` then the full `go test ./internal/cmd/ ./internal/tui/`.
Expected: PASS, no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/backend.go internal/tui/model_test.go internal/cmd/tui_backend.go internal/cmd/tui.go internal/cmd/tui_test.go
git commit -m "Route workspace rows through backend session actions"
```

---

### Task 7: TUI rendering and action guards for workspace rows

**Files:**

- Modify: `internal/tui/list.go` (`rowRepoName`, `rowBranch`, `rowPath`, `formatMachines`, `formatRowSync`, `formatRowChanges`/CHANGES + HEADS + ACTIVITY formatters, `formatWorkspace`, filter haystack)
- Modify: `internal/tui/model.go` (`openSelected`, `shellSelected`, `startKill`, `startDelete` + confirm flow, `startNewBranch`, `syncSelected`)
- Test: `internal/tui/list_test.go`, `internal/tui/model_test.go`

**Interfaces:**

- Consumes: `Row.Workspace` (Task 5), `Backend.UnregisterWorkspace` (Task 6).
- Produces: workspace rows render as REPO=name, BRANCH=tilde-abbreviated path, MACHINES=`local`, CHANGES=`-`, HEADS=`-`, ACTIVITY=`-`, WORKSPACE=live/offline; `confirmUnregister` confirm kind; git actions on workspace rows show status-line messages.

- [ ] **Step 1: Write failing tests.** In `internal/tui/list_test.go`:

```go
func TestWorkspaceRowRendering(t *testing.T) {
	home, _ := os.UserHomeDir()
	row := Row{
		Workspace:   &WorkspaceInfo{Name: "notes", Path: filepath.Join(home, "notes")},
		SessionLive: true,
	}

	assert.Equal(t, "notes", rowRepoName(row))
	assert.Equal(t, "~/notes", rowBranch(row))
	assert.Equal(t, filepath.Join(home, "notes"), rowPath(row))
	assert.Equal(t, "local", formatMachines(row))
	assert.Equal(t, "live", formatWorkspace(row))
}
```

(Tilde abbreviation comes from the existing `utils.TildePath` at `internal/utils/utils.go:97` — no new helper needed.)

In `internal/tui/model_test.go`:

```go
func TestWorkspaceRowActions(t *testing.T) {
	row := Row{
		Workspace:   &WorkspaceInfo{Name: "notes", Path: "/Users/me/notes"},
		SessionName: "kwt-workspace-dir-notes-12345678",
		SessionLive: true,
	}
	backend := &fakeBackend{}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	// Open: workspace rows hand off to attach outside tmux.
	next, _ := model.openSelected()
	assert.Equal(t, HandoffAttach, next.Handoff().Kind)

	// New branch: gated with a message.
	next, _ = model.startNewBranch()
	assert.Contains(t, next.message, "not a git worktree")

	// Sync: already gated by the row.Fleet == nil branch.
	next, _ = model.syncSelected(row)
	assert.Contains(t, next.message, "nothing to sync")

	// Kill: allowed for live sessions.
	next, _ = model.startKill()
	assert.Equal(t, confirmKill, next.confirm.kind)

	// Delete key: offers unregister, never worktree removal.
	next, _ = model.startDelete()
	require.Equal(t, confirmUnregister, next.confirm.kind)
	assert.Contains(t, next.confirm.text, "unregister")

	// Confirming with y calls Backend.UnregisterWorkspace and refreshes.
	_, cmd := updateModel(t, next, press("y"))
	require.NotNil(t, cmd)
	msg := cmd()
	done, ok := msg.(actionDoneMsg)
	require.True(t, ok)
	assert.True(t, done.refresh)
	require.Len(t, backend.unregistered, 1)
	assert.Equal(t, "notes", backend.unregistered[0].Workspace.Name)
}
```

(`press` and `updateModel` are existing helpers in `model_test.go`; the `y` press routes through `handleConfirmKey` at `internal/tui/model.go:471`, which is where Step 3 adds the `confirmUnregister` case.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run "TestWorkspaceRow" -v`
Expected: FAIL (rendering falls through to empty strings; `confirmUnregister` undefined → compile error first).

- [ ] **Step 3: Implement.** In `internal/tui/list.go`:

1. `rowRepoName`: first branch:

```go
	if row.Workspace != nil {
		return row.Workspace.Name
	}
```

2. `rowBranch`: first branch (add `go.kenn.io/kwt/internal/utils` to imports):

```go
	if row.Workspace != nil {
		return utils.TildePath(row.Workspace.Path)
	}
```

3. `rowPath`: first branch:

```go
	if row.Workspace != nil {
		return row.Workspace.Path
	}
```

4. `formatMachines`: workspace rows have `row.Fleet == nil`, which already returns `"local"` — verify, don't change.
5. CHANGES/HEADS/ACTIVITY formatters: locate the three formatter functions used by the table builder near `internal/tui/list.go:487` and add a leading `if row.Workspace != nil { return "-" }` to each (CHANGES, HEADS/sync, ACTIVITY). For `formatRowSync` specifically the guard must come before the `"local only"` fallback.
6. `formatWorkspace`: no change needed (`SessionLive` drives live/offline; the `Entry == nil && Fleet != nil` remote branch doesn't hit workspace rows) — verify with the rendering test.
7. Filter haystack: `filterRows`/`rowFleetHaystack` build search text from `rowRepoName`, `rowBranch`, `rowPath` — with the branches above, name and path are already included; verify by reading `filterRows` and add `row.Workspace.Name`/`Path` explicitly only if it composes differently.

In `internal/tui/model.go`:

1. Add `confirmUnregister` to the confirm-kind const block after `confirmKill`.
2. `openSelected`: change the guard to

```go
	if row.Entry == nil && row.Workspace == nil {
```

(keep the fleet materialize hint inside). 3. `shellSelected`: same guard change. 4. `startKill`: change `if row.Entry == nil` to `if row.Entry == nil && row.Workspace == nil`. 5. `startNewBranch`: add after the existing selection:

```go
	if row.Workspace != nil {
		m.message = "not a git worktree"
		return m, nil
	}
```

6. `syncSelected`: already returns "nothing to sync for this row" when `row.Fleet == nil` — verify covers workspace rows; no change expected.
7. `startDelete`: add before the nil-Entry guard:

```go
	if row.Workspace != nil {
		m.confirm = confirmState{
			kind: confirmUnregister,
			row:  row,
			text: fmt.Sprintf("unregister workspace %s? (directory is kept) [y/N]", row.Workspace.Name),
		}
		return m, nil
	}
```

8. In `handleConfirmKey` (`internal/tui/model.go:471`), add a case to the `switch kind` block alongside `confirmDelete`/`confirmKill`:

```go
	case confirmUnregister:
		return m, m.unregisterWorkspaceCmd(row)
```

and the command, modeled on `removeWorktreeCmd`:

```go
func (m Model) unregisterWorkspaceCmd(row Row) tea.Cmd {
	return func() tea.Msg {
		if err := m.backend.UnregisterWorkspace(row); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{message: "workspace unregistered", refresh: true}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -v -run TestWorkspaceRow`, then full `go test ./internal/tui/ ./internal/cmd/`.
Expected: PASS, no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/list.go internal/tui/model.go internal/tui/list_test.go internal/tui/model_test.go
git commit -m "Render and gate workspace rows in the dashboard"
```

---

### Task 8: End-to-end check, lint, docs touch-up

**Files:**

- Modify: `docs/reference/` or `README.md` only if a command list enumerates subcommands (check `rg -n "kwt tmux|kwt sync" README.md docs/` and mirror the style for `kwt workspace`)
- Test: whole tree

- [ ] **Step 1: Full verification**

Run:

```bash
go build ./... && go test ./... && go vet ./... && golangci-lint run
```

Expected: all pass, `0 issues`.

- [ ] **Step 2: Manual smoke test** (real tmux required; skip pane assertions if no tmux on PATH):

```bash
go build -o /tmp/kwt-dw ./cmd/kwt
mkdir -p /tmp/kwt-demo-notes
KWT_HOME=$(mktemp -d) /tmp/kwt-dw workspace add /tmp/kwt-demo-notes --name demo-notes
KWT_HOME=<same dir> /tmp/kwt-dw workspace list      # expect demo-notes ... stopped
KWT_HOME=<same dir> /tmp/kwt-dw workspace remove demo-notes
```

Expected: registered → listed stopped → unregistered; `remove` of a second, unknown name errors listing nothing registered.

- [ ] **Step 3: Docs** — if README/docs enumerate command groups, add one line for `kwt workspace` following the existing style. Do not write new doc pages.

- [ ] **Step 4: Commit** (only if docs changed; otherwise skip)

```bash
git add README.md docs/
git commit -m "Document kwt workspace command group"
```
