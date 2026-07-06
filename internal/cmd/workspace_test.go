package cmd

import (
	"errors"
	"os"
	"path/filepath"
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
	// Normalize paths to handle macOS symlink differences (/var vs /private/var)
	expectedPath, _ := filepath.EvalSymlinks(dir)
	gotPath, _ := filepath.EvalSymlinks(got.Path)
	assert.Equal(t, expectedPath, gotPath)
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

func tmuxDirSessionNameForTest(name, path string) string {
	return tmux.DirWorkspaceSessionName(name, path)
}
