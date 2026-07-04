package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/discovery"
	"go.kenn.io/kwt/internal/fleet"
	dashboard "go.kenn.io/kwt/internal/tui"
	"go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/pkg/models"
)

func TestTUICmdIsolatesFromCwdConfig(t *testing.T) {
	require.NotNil(t, tuiCmd.PersistentPreRunE,
		"tui must define its own PersistentPreRunE to bypass root's cwd merge")
	require.NoError(t, tuiCmd.PersistentPreRunE(tuiCmd, nil),
		"tui's PersistentPreRunE must be a no-op that never errors")
}

func TestRunTUIRejectsNonInteractiveTerminal(t *testing.T) {
	oldStdin, oldStdout := stdinIsTerminal, stdoutIsTerminal
	defer func() {
		stdinIsTerminal = oldStdin
		stdoutIsTerminal = oldStdout
	}()
	stdinIsTerminal = func() bool { return false }
	stdoutIsTerminal = func() bool { return true }

	err := runTUI(tuiCmd, nil)

	require.Error(t, err)
	assert.EqualError(t, err, "kwt tui requires an interactive terminal")
}

func TestRootPersistentPreRunSkipsCwdConfigForBareRoot(t *testing.T) {
	oldMerge := mergeCwdLocal
	defer func() { mergeCwdLocal = oldMerge }()
	called := false
	mergeCwdLocal = func() error {
		called = true
		return nil
	}

	require.NoError(t, rootCmd.PersistentPreRunE(rootCmd, nil))

	assert.False(t, called, "bare root command must not merge cwd local config before launching global TUI")
}

func TestRootPersistentPreRunMergesCwdConfigForSubcommands(t *testing.T) {
	oldMerge := mergeCwdLocal
	defer func() { mergeCwdLocal = oldMerge }()
	called := false
	mergeCwdLocal = func() error {
		called = true
		return nil
	}

	require.NoError(t, rootCmd.PersistentPreRunE(statusCmd, nil))

	assert.True(t, called, "ordinary subcommands must still merge cwd local config")
}

func TestRootPersistentPreRunReturnsMergeError(t *testing.T) {
	oldMerge := mergeCwdLocal
	defer func() { mergeCwdLocal = oldMerge }()
	mergeCwdLocal = func() error { return errors.New("merge failed") }

	err := rootCmd.PersistentPreRunE(statusCmd, nil)

	require.Error(t, err)
	assert.EqualError(t, err, "merge failed")
}

func TestRootArgsRejectsUnknownBareArgs(t *testing.T) {
	require.NotNil(t, rootCmd.Args)
	assert.NoError(t, rootCmd.Args(rootCmd, nil))
	assert.Error(t, rootCmd.Args(rootCmd, []string{"unknown"}))
}

func TestRootRunPrintsHelpWhenNotInteractive(t *testing.T) {
	oldStdin, oldStdout, oldRun := stdinIsTerminal, stdoutIsTerminal, runRootTUI
	defer func() {
		stdinIsTerminal = oldStdin
		stdoutIsTerminal = oldStdout
		runRootTUI = oldRun
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	}()
	stdinIsTerminal = func() bool { return false }
	stdoutIsTerminal = func() bool { return false }
	launched := false
	runRootTUI = func(cmd *cobra.Command, args []string) error {
		launched = true
		return nil
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)

	require.NoError(t, runRoot(rootCmd, nil))

	assert.False(t, launched)
	assert.Contains(t, out.String(), "kwt is a CLI tool")
}

func TestRootRunLaunchesTUIWhenInteractive(t *testing.T) {
	oldStdin, oldStdout, oldRun := stdinIsTerminal, stdoutIsTerminal, runRootTUI
	defer func() {
		stdinIsTerminal = oldStdin
		stdoutIsTerminal = oldStdout
		runRootTUI = oldRun
	}()
	stdinIsTerminal = func() bool { return true }
	stdoutIsTerminal = func() bool { return true }
	launched := false
	runRootTUI = func(cmd *cobra.Command, args []string) error {
		launched = true
		return nil
	}

	require.NoError(t, runRoot(rootCmd, nil))

	assert.True(t, launched)
}

func TestBuildTUIRowSkipsSessionNameWhenRepositoryInfoMissing(t *testing.T) {
	entry := &discovery.GlobalWorktreeEntry{
		Branch: "detached",
		Path:   "/work/odd-detached",
	}
	status := &models.WorktreeStatus{Path: entry.Path, Branch: entry.Branch}

	row := buildTUIRow(entry, status, map[string]bool{"": true})

	assert.Equal(t, entry, row.Entry)
	assert.Equal(t, status, row.Status)
	assert.Empty(t, row.SessionName)
	assert.False(t, row.SessionLive)
}

func TestBuildTUIRowMarksLiveSessionWhenRepositoryInfoPresent(t *testing.T) {
	entry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{Host: "github.com", Owner: "example", Repository: "kwt"},
		Branch:         "feature",
		Path:           "/work/kwt/feature",
	}
	status := &models.WorktreeStatus{Path: entry.Path, Branch: entry.Branch}

	row := buildTUIRow(entry, status, map[string]bool{
		"kwt-workspace-github-com-example-kwt-feature-": false,
	})

	assert.NotEmpty(t, row.SessionName)
	assert.False(t, row.SessionLive)

	row = buildTUIRow(entry, status, map[string]bool{row.SessionName: true})
	assert.True(t, row.SessionLive)
}

func TestTUIStatusCollectorOptionsFetchesSyncState(t *testing.T) {
	opts := tuiStatusCollectorOptions("/worktrees")

	assert.True(t, opts.FetchRemote)
	assert.Equal(t, "/worktrees", opts.BaseDir)
}

func TestTUIBackendListIncludesLaunchRepositoryWorktrees(t *testing.T) {
	cfg := &models.Config{Worktree: models.WorktreeConfig{BaseDir: "/global"}}
	globalEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{Host: "github.com", Owner: "example", Repository: "kwt"},
		Branch:         "main",
		Path:           "/global/github.com/example/kwt/main",
	}
	launchEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{Host: "github.com", Owner: "example", Repository: "other"},
		Branch:         "main",
		Path:           "/repos/other",
		IsMain:         true,
	}
	backend := newTUIBackendWithLaunchDir(cfg, "/repos/other")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		assert.Equal(t, "/global", baseDir)
		return []*discovery.GlobalWorktreeEntry{globalEntry}, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		assert.Equal(t, "/repos/other", launchDir)
		return []*discovery.GlobalWorktreeEntry{launchEntry}, nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		assert.Equal(t, "/global", baseDir)
		assert.Len(t, entries, 2)
		return map[string]*models.WorktreeStatus{
			globalEntry.Path: {Path: globalEntry.Path, Branch: globalEntry.Branch},
			launchEntry.Path: {Path: launchEntry.Path, Branch: launchEntry.Branch, IsCurrent: true},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.ElementsMatch(t, []string{globalEntry.Path, launchEntry.Path}, []string{
		rowPathForHandoff(rows[0]),
		rowPathForHandoff(rows[1]),
	})
	assert.True(t, rows[1].Status.IsCurrent)
}

func TestTUIBackendListIncludesRegisteredProjectWorktrees(t *testing.T) {
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: "/global"},
		Projects: []models.Project{{
			Repository: "github.com/example/tools",
			Name:       "other",
			Path:       "/repos/other",
		}},
	}
	globalEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{Host: "github.com", Owner: "example", Repository: "kwt"},
		Branch:         "main",
		Path:           "/global/github.com/example/kwt/main",
	}
	projectEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{Host: "github.com", Owner: "example", Repository: "other"},
		Branch:         "feature",
		Path:           "/repos/other-feature",
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		assert.Equal(t, "/global", baseDir)
		return []*discovery.GlobalWorktreeEntry{globalEntry}, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		assert.Equal(t, "/repos/other", projectPath)
		return []*discovery.GlobalWorktreeEntry{projectEntry}, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		require.Empty(t, launchDir)
		return nil, nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		assert.ElementsMatch(t, []string{globalEntry.Path, projectEntry.Path}, []string{
			entries[0].Path,
			entries[1].Path,
		})
		return map[string]*models.WorktreeStatus{
			globalEntry.Path:  {Path: globalEntry.Path, Branch: globalEntry.Branch},
			projectEntry.Path: {Path: projectEntry.Path, Branch: projectEntry.Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.ElementsMatch(t, []string{globalEntry.Path, projectEntry.Path}, []string{
		rowPathForHandoff(rows[0]),
		rowPathForHandoff(rows[1]),
	})
}

func TestTUIBackendListIncludesRemoteOnlyFleetRows(t *testing.T) {
	observedAt := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: "/global"},
		Fleet:    models.FleetConfig{Enabled: true, HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "github.com/example/kwt",
			Name:       "kwt",
			Path:       "/repos/kwt",
		}},
	}
	localEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "kwt",
			FullPath:   "github.com/example/kwt",
		},
		Branch: "main",
		Path:   "/repos/kwt",
		IsMain: true,
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return []*discovery.GlobalWorktreeEntry{localEntry}, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return map[string]*models.WorktreeStatus{
			localEntry.Path: {Path: localEntry.Path, Branch: localEntry.Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }
	backend.readFleetState = func(context.Context, *models.Config) (fleet.FleetState, error) {
		return fleet.FleetState{Rows: []fleet.FleetRow{
			{
				ProjectIdentity: "github.com/example/kwt",
				ProjectName:     "kwt",
				Kind:            "branch",
				Ref:             "main",
				Branch:          "main",
				Observations: []fleet.Observation{{
					HostID:     "host-a",
					Path:       "/repos/kwt",
					Head:       "aaa",
					ObservedAt: observedAt,
				}},
			},
			{
				ProjectIdentity: "github.com/example/kwt",
				ProjectName:     "kwt",
				Kind:            "branch",
				Ref:             "feature/studio-only",
				Branch:          "feature/studio-only",
				Observations: []fleet.Observation{{
					HostID:     "host-b",
					Path:       "/work/host-b/kwt/feature-studio-only",
					Head:       "bbb",
					ObservedAt: observedAt,
				}},
			},
		}}, nil
	}

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 2)
	remote := rows[0]
	if remote.Fleet == nil || remote.Fleet.Ref != "feature/studio-only" {
		remote = rows[1]
	}
	require.NotNil(t, remote.Fleet)
	assert.Nil(t, remote.Entry)
	assert.Equal(t, "kwt", remote.Fleet.ProjectName)
	assert.Equal(t, "feature/studio-only", remote.Fleet.Branch)
	assert.False(t, remote.Fleet.Local)
	assert.Equal(t, []string{"host-b"}, remote.Fleet.Hosts)
	assert.Equal(t, "host-b", remote.Fleet.MaterializeHost)
	assert.Equal(t, "/work/host-b/kwt/feature-studio-only", remote.Fleet.RemotePath)
}

func TestTUIBackendListIncludesRegisteredProjectWithoutOrigin(t *testing.T) {
	repoPath := newTUITestRepo(t)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: "local.example/team/service",
			Name:       "service",
			Path:       repoPath,
		}},
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		require.Len(t, entries, 1)
		return map[string]*models.WorktreeStatus{
			entries[0].Path: {Path: entries[0].Path, Branch: entries[0].Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, samePath(repoPath, rowPathForHandoff(rows[0])))
	require.NotNil(t, rows[0].Entry.RepositoryInfo)
	assert.Equal(t, "local.example/team/service", rows[0].Entry.RepositoryInfo.FullPath)
	assert.Equal(t, "service", rows[0].Entry.RepositoryInfo.Repository)
}

func TestTUIBackendListPrefersRegisteredIdentityForGlobalLocalOnlyDuplicate(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "service")
	require.NoError(t, os.MkdirAll(repoPath, 0755))
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: "local.example/team/service",
			Name:       "service",
			Path:       repoPath,
		}},
	}
	globalEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: repositoryInfoFromRootPath(repoPath),
		Branch:         "main",
		Path:           repoPath,
		IsMain:         true,
	}
	projectEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: repositoryInfoFromRootPath(repoPath),
		Branch:         "main",
		Path:           repoPath,
		IsMain:         true,
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	stubTUIProjectRegistration(backend)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return []*discovery.GlobalWorktreeEntry{globalEntry}, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		assert.Equal(t, repoPath, projectPath)
		return []*discovery.GlobalWorktreeEntry{projectEntry}, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		require.Len(t, entries, 1)
		require.NotNil(t, entries[0].RepositoryInfo)
		assert.Equal(t, "local.example/team/service", entries[0].RepositoryInfo.FullPath)
		return map[string]*models.WorktreeStatus{
			entries[0].Path: {Path: entries[0].Path, Branch: entries[0].Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Entry.RepositoryInfo)
	assert.Equal(t, "local.example/team/service", rows[0].Entry.RepositoryInfo.FullPath)
	assert.Equal(t, "service", rows[0].Entry.RepositoryInfo.Repository)
}

func TestTUIBackendListRegistersLaunchRepositoryBestEffort(t *testing.T) {
	cfg := &models.Config{Worktree: models.WorktreeConfig{BaseDir: "/global"}}
	launchEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "other",
			FullPath:   "github.com/example/tools",
		},
		Branch: "main",
		Path:   "/repos/other",
		IsMain: true,
	}
	var registered []models.Project
	backend := newTUIBackendWithLaunchDir(cfg, "/repos/other")
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return []*discovery.GlobalWorktreeEntry{launchEntry}, nil
	}
	backend.registerProject = func(project models.Project) error {
		registered = append(registered, project)
		return errors.New("read-only config")
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return map[string]*models.WorktreeStatus{
			launchEntry.Path: {Path: launchEntry.Path, Branch: launchEntry.Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, registered, 1)
	assert.Equal(t, "github.com/example/tools", registered[0].Repository)
	assert.Equal(t, "other", registered[0].Name)
	assert.Equal(t, "/repos/other", registered[0].Path)
}

func TestTUIBackendListAddsLaunchRepositoryToInMemoryProjects(t *testing.T) {
	cfg := &models.Config{Worktree: models.WorktreeConfig{BaseDir: "/global"}}
	launchEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "other",
			FullPath:   "github.com/example/tools",
		},
		Branch: "main",
		Path:   "/repos/other",
		IsMain: true,
	}
	backend := newTUIBackendWithLaunchDir(cfg, "/repos/other")
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return []*discovery.GlobalWorktreeEntry{launchEntry}, nil
	}
	backend.registerProject = func(project models.Project) error {
		return nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return map[string]*models.WorktreeStatus{
			launchEntry.Path: {Path: launchEntry.Path, Branch: launchEntry.Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	rows, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, cfg.Projects, 1)
	assert.Equal(t, "github.com/example/tools", cfg.Projects[0].Repository)
	assert.Equal(t, "other", cfg.Projects[0].Name)
	assert.Equal(t, "/repos/other", cfg.Projects[0].Path)
}

func TestTUIBackendLaunchRegistrationReusesExistingProjectByPath(t *testing.T) {
	repoPath := newTUITestRepo(t)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: "local.example/team/service",
			Name:       "service",
			Path:       repoPath,
		}},
	}
	launchEntry := &discovery.GlobalWorktreeEntry{
		RepositoryInfo: repositoryInfoFromRootPath(repoPath),
		Branch:         "main",
		Path:           repoPath,
		IsMain:         true,
	}
	var registered []models.Project
	backend := newTUIBackendWithLaunchDir(cfg, repoPath)
	backend.discoverGlobalWorktrees = func(baseDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverProjectWorktrees = func(projectPath string) ([]*discovery.GlobalWorktreeEntry, error) {
		return nil, nil
	}
	backend.discoverLaunchWorktrees = func(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
		return []*discovery.GlobalWorktreeEntry{launchEntry}, nil
	}
	backend.registerProject = func(project models.Project) error {
		registered = append(registered, project)
		return nil
	}
	backend.collectStatuses = func(
		ctx context.Context,
		baseDir string,
		entries []*discovery.GlobalWorktreeEntry,
	) (map[string]*models.WorktreeStatus, error) {
		return map[string]*models.WorktreeStatus{
			launchEntry.Path: {Path: launchEntry.Path, Branch: launchEntry.Branch},
		}, nil
	}
	backend.listSessions = func() ([]string, error) { return nil, nil }

	_, err := backend.List(context.Background())

	require.NoError(t, err)
	require.Len(t, registered, 1)
	assert.Equal(t, "local.example/team/service", registered[0].Repository)
	assert.Equal(t, "service", registered[0].Name)
	assert.True(t, samePath(repoPath, registered[0].Path))
	require.Len(t, cfg.Projects, 1)
	assert.Equal(t, "local.example/team/service", cfg.Projects[0].Repository)
}

func TestTUIBackendLaunchRegistrationUpgradesPathFallbackToRemoteIdentity(t *testing.T) {
	repoPath := newTUITestRepo(t)
	pathFallback := repositoryInfoFromRootPath(repoPath).FullPath
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: pathFallback,
			Name:       filepath.Base(repoPath),
			Path:       repoPath,
		}},
	}
	launchEntry := &discovery.GlobalWorktreeEntry{
		RepositoryURL: "https://github.com/example/service-api.git",
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "service",
			FullPath:   "github.com/example/service-api",
		},
		Branch: "main",
		Path:   repoPath,
		IsMain: true,
	}
	var registered []models.Project
	backend := newTUIBackendWithLaunchDir(cfg, repoPath)
	backend.registerProject = func(project models.Project) error {
		registered = append(registered, project)
		return nil
	}

	backend.registerLaunchProject([]*discovery.GlobalWorktreeEntry{launchEntry})

	require.Len(t, registered, 1)
	assert.Equal(t, "github.com/example/service-api", registered[0].Repository)
	assert.Equal(t, "service", registered[0].Name)
	assert.True(t, samePath(repoPath, registered[0].Path))
	require.Len(t, cfg.Projects, 1)
	assert.Equal(t, "github.com/example/service-api", cfg.Projects[0].Repository)
}

func TestHasStableProjectIdentityRejectsAbsolutePathFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		want       bool
	}{
		{name: "remote full path", repository: "github.com/example/service-api", want: true},
		{name: "path safe local identity", repository: "local/Users/test/service", want: true},
		{name: "relative project identity", repository: "workspace/service", want: true},
		{name: "unix absolute path", repository: "/Users/test/service", want: false},
		{name: "unix absolute tmp path", repository: "/var/tmp/service", want: false},
		{name: "windows slash absolute path", repository: `C:/Users/test/service`, want: false},
		{name: "windows backslash absolute path", repository: `C:\Users\test\service`, want: false},
		{name: "windows unc absolute path", repository: `\\server\share\service`, want: false},
		{name: "slash unc absolute path", repository: "//server/share/service", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasStableProjectIdentity(models.Project{Repository: tt.repository})

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTUIBackendRemoveWorktreeFallsBackToRegisteredProjectRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repoPath := newTUITestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "stale-worktree")
	runTUITestGit(t, repoPath, "worktree", "add", "-b", "codex/stale", worktreePath)
	require.NoError(t, os.RemoveAll(worktreePath))

	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: "github.com/example/service-api",
			Name:       "service-api",
			Path:       repoPath,
		}},
	}
	row := dashboard.Row{Entry: &discovery.GlobalWorktreeEntry{
		RepositoryURL: "https://github.com/example/service-api.git",
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "service-api",
			FullPath:   "github.com/example/service-api",
		},
		Branch: "codex/stale",
		Path:   worktreePath,
	}}
	backend := newTUIBackendWithLaunchDir(cfg, "")

	err := backend.RemoveWorktree(context.Background(), row)

	require.NoError(t, err)
	output := runTUITestGitOutput(t, repoPath, "worktree", "list", "--porcelain")
	assert.NotContains(t, output, worktreePath)
}

func TestTUIBackendMaterializeWorktreeUsesRegisteredProjectRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repoPath := newTUITestRepo(t)
	runTUITestGit(t, repoPath, "remote", "add", "origin", "https://github.com/example/kwt.git")
	runTUITestGit(t, repoPath, "branch", "feature/studio-only")
	baseDir := filepath.Join(t.TempDir(), "worktrees")
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: baseDir, AutoMkdir: true},
		Projects: []models.Project{{
			Repository: "github.com/example/kwt",
			Name:       "kwt",
			Path:       repoPath,
		}},
	}
	backend := newTUIBackendWithLaunchDir(cfg, "")
	row := dashboard.Row{Fleet: &dashboard.FleetInfo{
		ProjectIdentity: "github.com/example/kwt",
		ProjectName:     "kwt",
		Kind:            "branch",
		Ref:             "feature/studio-only",
		Branch:          "feature/studio-only",
		Hosts:           []string{"host-b"},
	}}

	path, err := backend.MaterializeWorktree(context.Background(), row)

	require.NoError(t, err)
	assert.DirExists(t, path)
	assert.True(t, strings.HasPrefix(path, baseDir), path)
	branch := strings.TrimSpace(runTUITestGitOutput(t, path, "branch", "--show-current"))
	assert.Equal(t, "feature/studio-only", branch)
}

func TestTUIBackendRemoveWorktreeRepairsBrokenGitFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repoPath := newTUITestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "broken-worktree")
	runTUITestGit(t, repoPath, "worktree", "add", "-b", "codex/broken", worktreePath)
	require.NoError(t, os.WriteFile(
		filepath.Join(worktreePath, ".git"),
		[]byte("gitdir: /missing/repo/.git/worktrees/broken-worktree\n"),
		0644,
	))

	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Projects: []models.Project{{
			Repository: "github.com/example/service-api",
			Name:       "service-api",
			Path:       repoPath,
		}},
	}
	row := dashboard.Row{Entry: &discovery.GlobalWorktreeEntry{
		RepositoryURL: "https://github.com/example/service-api.git",
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "service-api",
			FullPath:   "github.com/example/service-api",
		},
		Branch: "codex/broken",
		Path:   worktreePath,
	}}
	backend := newTUIBackendWithLaunchDir(cfg, "")

	err := backend.RemoveWorktree(context.Background(), row)

	require.NoError(t, err)
	output := runTUITestGitOutput(t, repoPath, "worktree", "list", "--porcelain")
	assert.NotContains(t, output, worktreePath)
	assert.NoDirExists(t, worktreePath)
}

func TestRepairLinkedWorktreeGitFileRejectsSymlink(t *testing.T) {
	repoPath := newTUITestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "symlink-worktree")
	runTUITestGit(t, repoPath, "worktree", "add", "-b", "codex/symlink", worktreePath)

	victimPath := filepath.Join(t.TempDir(), "victim")
	const victimContents = "do not replace me\n"
	require.NoError(t, os.WriteFile(victimPath, []byte(victimContents), 0644))

	gitFilePath := filepath.Join(worktreePath, ".git")
	require.NoError(t, os.Remove(gitFilePath))
	if err := os.Symlink(victimPath, gitFilePath); err != nil {
		t.Skipf("symbolic links are not supported or allowed on this filesystem: %v", err)
	}

	err := repairLinkedWorktreeGitFile(repoPath, worktreePath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbolic link")
	data, readErr := os.ReadFile(victimPath)
	require.NoError(t, readErr)
	assert.Equal(t, victimContents, string(data))
}

func TestRepairLinkedWorktreeGitFileReplacesHardLinkWithoutClobberingTarget(t *testing.T) {
	repoPath := newTUITestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "hardlink-worktree")
	runTUITestGit(t, repoPath, "worktree", "add", "-b", "codex/hardlink", worktreePath)

	victimPath := filepath.Join(t.TempDir(), "victim")
	const victimContents = "do not replace me\n"
	require.NoError(t, os.WriteFile(victimPath, []byte(victimContents), 0644))

	gitFilePath := filepath.Join(worktreePath, ".git")
	require.NoError(t, os.Remove(gitFilePath))
	if err := os.Link(victimPath, gitFilePath); err != nil {
		t.Skipf("hard links are not supported on this filesystem: %v", err)
	}

	err := repairLinkedWorktreeGitFile(repoPath, worktreePath)

	require.NoError(t, err)
	victimData, readErr := os.ReadFile(victimPath)
	require.NoError(t, readErr)
	assert.Equal(t, victimContents, string(victimData))
	gitData, readErr := os.ReadFile(gitFilePath)
	require.NoError(t, readErr)
	assert.Contains(t, string(gitData), "gitdir: ")
	assert.NotEqual(t, string(gitData), string(victimData))
}

func TestTUIBackendResolveLayoutFallsBackToRegisteredProjectRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repoPath := newTUITestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "broken-worktree")
	runTUITestGit(t, repoPath, "worktree", "add", "-b", "codex/broken-layout", worktreePath)
	require.NoError(t, os.WriteFile(
		filepath.Join(worktreePath, ".git"),
		[]byte("gitdir: /missing/repo/.git/worktrees/broken-worktree\n"),
		0644,
	))

	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "global")},
		Agents: map[string]string{
			"codex": "codex --profile kwt",
		},
		Layouts: models.LayoutsConfig{
			Default: "quad",
			Presets: []models.Layout{{
				Name:    "quad",
				Arrange: "tiled",
				Panes:   []string{"agent:codex"},
			}},
		},
		Projects: []models.Project{{
			Repository: "github.com/example/service-api",
			Name:       "service-api",
			Path:       repoPath,
		}},
	}
	row := dashboard.Row{Entry: &discovery.GlobalWorktreeEntry{
		RepositoryURL: "https://github.com/example/service-api.git",
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "example",
			Repository: "service-api",
			FullPath:   "github.com/example/service-api",
		},
		Branch: "codex/broken-layout",
		Path:   worktreePath,
	}}
	backend := newTUIBackendWithLaunchDir(cfg, "")

	layout, err := backend.resolveLayout(row, "", false)

	require.NoError(t, err)
	assert.Equal(t, []string{"codex --profile kwt"}, layout.Panes)
}

func TestProjectMatchesRowRejectsDuplicateBasenameIdentityMismatch(t *testing.T) {
	row := dashboard.Row{Entry: &discovery.GlobalWorktreeEntry{
		RepositoryInfo: &url.RepositoryInfo{
			Host:       "github.com",
			Owner:      "org-two",
			Repository: "service",
			FullPath:   "github.com/org-two/service",
		},
	}}

	assert.False(t, projectMatchesRow(models.Project{
		Repository: "github.com/org-one/service",
		Name:       "service",
		Path:       "/repos/org-one/service",
	}, row))
	assert.True(t, projectMatchesRow(models.Project{
		Repository: "github.com/org-two/service",
		Name:       "service",
		Path:       "/repos/org-two/service",
	}, row))
}

func TestDiscoverLaunchRepoWorktreesListsLocalOnlyRepository(t *testing.T) {
	repoPath := newTUITestRepo(t)

	entries, err := discoverLaunchRepoWorktrees(repoPath)

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, samePath(repoPath, entries[0].Path))
	assert.Equal(t, "main", entries[0].Branch)
	assert.True(t, entries[0].IsMain)
	assert.Empty(t, entries[0].RepositoryURL)
	require.NotNil(t, entries[0].RepositoryInfo)
	assert.Equal(t, filepath.ToSlash(cleanComparablePath(repoPath)), entries[0].RepositoryInfo.FullPath)
	assert.Equal(t, filepath.Base(repoPath), entries[0].RepositoryInfo.Repository)
}

func newTUITestRepo(t *testing.T) string {
	t.Helper()

	repoPath := filepath.Join(t.TempDir(), "repo")
	runTUITestGit(t, "", "init", "-b", "main", repoPath)
	runTUITestGit(t, repoPath, "config", "user.name", "Test User")
	runTUITestGit(t, repoPath, "config", "user.email", "test@example.com")

	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Test Repository\n"), 0644))
	runTUITestGit(t, repoPath, "add", ".")
	runTUITestGit(t, repoPath, "commit", "-m", "Initial commit")
	return repoPath
}

func runTUITestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runTUITestGitOutput(t, dir, args...)
}

func runTUITestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, fmt.Sprintf("git %s failed:\n%s", strings.Join(args, " "), output))
	return string(output)
}

func stubTUIProjectRegistration(backend *tuiBackend) {
	backend.registerProject = func(models.Project) error { return nil }
}
