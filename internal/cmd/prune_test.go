package cmd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/registry"
	"go.kenn.io/kwt/pkg/models"
)

func TestPrunePublishesAfterSuccessfulNormalPrune(t *testing.T) {
	resetFleetCommandDeps(t)
	resetPruneCommandFlags(t)

	repoPath := newTUITestRepo(t)
	initCommandTestConfig(t, t.TempDir())
	t.Chdir(repoPath)

	var calls int
	publishFleetBestEffortForCommand = func(*cobra.Command, *models.Config) {
		calls++
	}

	cmd, _, _ := fleetTestCommand()
	err := runPrune(cmd, nil)

	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestPruneDoesNotPublishWhenNormalPruneFails(t *testing.T) {
	resetFleetCommandDeps(t)
	resetPruneCommandFlags(t)

	initCommandTestConfig(t, t.TempDir())
	t.Chdir(t.TempDir())

	var calls int
	publishFleetBestEffortForCommand = func(*cobra.Command, *models.Config) {
		calls++
	}

	cmd, _, _ := fleetTestCommand()
	err := runPrune(cmd, nil)

	require.Error(t, err)
	assert.Zero(t, calls)
}

func TestPruneExpiredPublishesOnceAfterUnregisteringExpiredEntry(t *testing.T) {
	resetFleetCommandDeps(t)
	resetPruneCommandFlags(t)

	initCommandTestConfig(t, t.TempDir())
	pruneExpired = true
	expiredPath := filepath.Join(t.TempDir(), "missing-expired")
	registerExpiredWorktree(t, expiredPath)

	var calls int
	publishFleetBestEffortForCommand = func(*cobra.Command, *models.Config) {
		calls++
	}

	cmd, _, _ := fleetTestCommand()
	err := runPrune(cmd, nil)

	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	reg, err := registry.New()
	require.NoError(t, err)
	_, exists := reg.Get(expiredPath)
	assert.False(t, exists)
}

func TestPruneExpiredDoesNotPublishOnNoop(t *testing.T) {
	resetFleetCommandDeps(t)
	resetPruneCommandFlags(t)

	initCommandTestConfig(t, t.TempDir())
	pruneExpired = true

	var calls int
	publishFleetBestEffortForCommand = func(*cobra.Command, *models.Config) {
		calls++
	}

	cmd, _, _ := fleetTestCommand()
	err := runPrune(cmd, nil)

	require.NoError(t, err)
	assert.Zero(t, calls)
}

func TestPruneExpiredDoesNotPublishOnDryRun(t *testing.T) {
	resetFleetCommandDeps(t)
	resetPruneCommandFlags(t)

	initCommandTestConfig(t, t.TempDir())
	pruneExpired = true
	pruneDryRun = true
	expiredPath := filepath.Join(t.TempDir(), "missing-expired-dry-run")
	registerExpiredWorktree(t, expiredPath)

	var calls int
	publishFleetBestEffortForCommand = func(*cobra.Command, *models.Config) {
		calls++
	}

	cmd, _, _ := fleetTestCommand()
	err := runPrune(cmd, nil)

	require.NoError(t, err)
	assert.Zero(t, calls)
	reg, err := registry.New()
	require.NoError(t, err)
	_, exists := reg.Get(expiredPath)
	assert.True(t, exists)
}

func resetPruneCommandFlags(t *testing.T) {
	t.Helper()

	oldPruneExpired := pruneExpired
	oldPruneDryRun := pruneDryRun
	oldPruneForce := pruneForce

	t.Cleanup(func() {
		pruneExpired = oldPruneExpired
		pruneDryRun = oldPruneDryRun
		pruneForce = oldPruneForce
	})

	pruneExpired = false
	pruneDryRun = false
	pruneForce = false
}

func registerExpiredWorktree(t *testing.T, path string) {
	t.Helper()

	reg, err := registry.New()
	require.NoError(t, err)
	expiredAt := time.Now().Add(-time.Hour)
	require.NoError(t, reg.Register(&registry.WorktreeEntry{
		Repository: "repo",
		Branch:     "task7/expired",
		Path:       path,
		ExpiresAt:  &expiredAt,
	}))
}
