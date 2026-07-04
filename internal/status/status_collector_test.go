package status

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/pkg/models"
)

func TestCollectAllMarksCurrentPathByDirectoryBoundary(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main")
	mainFixPath := filepath.Join(root, "main-fix")
	require.NoError(t, os.MkdirAll(mainPath, 0755))
	require.NoError(t, os.MkdirAll(mainFixPath, 0755))
	changeDir(t, mainFixPath)

	collector := NewStatusCollectorWithOptions(StatusCollectorOptions{})
	statuses, err := collector.CollectAll(context.Background(), []*models.Worktree{
		{Path: mainPath, Branch: "main"},
		{Path: mainFixPath, Branch: "main-fix"},
	})

	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.False(t, statuses[0].IsCurrent)
	assert.True(t, statuses[1].IsCurrent)
}

func TestCollectAllUsesRemoteFullPathForNestedRepository(t *testing.T) {
	baseDir := t.TempDir()
	worktreePath := filepath.Join(baseDir, "gitlab.com", "org", "team", "service", "feature-read-api")
	require.NoError(t, os.MkdirAll(worktreePath, 0755))
	runStatusTestGit(t, worktreePath, "init", "-b", "main")
	runStatusTestGit(t, worktreePath, "config", "user.name", "Test User")
	runStatusTestGit(t, worktreePath, "config", "user.email", "test@example.com")
	runStatusTestGit(t, worktreePath, "remote", "add", "origin", "https://gitlab.com/org/team/service.git")
	require.NoError(t, os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# service\n"), 0644))
	runStatusTestGit(t, worktreePath, "add", ".")
	runStatusTestGit(t, worktreePath, "commit", "-m", "Initial commit")

	collector := NewStatusCollectorWithOptions(StatusCollectorOptions{BaseDir: baseDir})
	statuses, err := collector.CollectAll(context.Background(), []*models.Worktree{
		{Path: worktreePath, Branch: "feature/read-api"},
	})

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "gitlab.com/org/team/service", statuses[0].Repository)
}

func TestRepositoryFullPathIdentityNormalizesWindowsSeparators(t *testing.T) {
	info := &url.RepositoryInfo{FullPath: `gitlab.com\org\team\service`}

	assert.Equal(t, "gitlab.com/org/team/service", repositoryFullPathIdentity(info))
}

func TestGetLastActivityFallbackHonorsCanceledContext(t *testing.T) {
	collector := NewStatusCollectorWithOptions(StatusCollectorOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := collector.getLastActivityFallback(ctx, t.TempDir())

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func changeDir(t *testing.T, dir string) {
	t.Helper()

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(oldWd))
	})
}

func runStatusTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
}
