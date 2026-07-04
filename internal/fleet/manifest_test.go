package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/pkg/models"
)

var fixedTime = time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

func TestBuildManifestIncludesConfiguredProjectWorktrees(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")
	wtPath := filepath.Join(t.TempDir(), "feature-fleet")
	runGit(t, repo, "worktree", "add", "-b", "feature/fleet", wtPath)

	cfg := &models.Config{Fleet: models.FleetConfig{HostID: "host-a"}, Projects: []models.Project{{
		Repository: "github.com/kenn-io/kwt",
		Name:       "kwt",
		Path:       repo,
	}}}
	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, 1, manifest.SchemaVersion)
	assert.Equal(t, "host-a", manifest.HostID)
	assert.Equal(t, fixedTime, manifest.ObservedAt)
	assert.Contains(t, worktreeRefs(manifest), "branch:feature/fleet")
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
}

func TestBuildManifestKeysDetachedWorktreeByHead(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")
	head := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	wtPath := filepath.Join(t.TempDir(), "detached")
	runGit(t, repo, "worktree", "add", "--detach", wtPath, head)

	manifest, err := buildTestManifestForRepo(t, repo)
	require.NoError(t, err)
	detached := findKind(t, manifest, "detached")
	assert.Equal(t, head, detached.Ref)
	assert.Equal(t, head, detached.Head)
}

func TestBuildManifestIncludesDirtyCountsAndLastActivity(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# changed\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("untracked\n"), 0644))

	manifest, err := buildTestManifestForRepo(t, repo)
	require.NoError(t, err)
	main := findBranch(t, manifest, "main")
	assert.Equal(t, ChangeStatus{Modified: 1, Untracked: 1}, main.Status)
	assert.False(t, main.LastActivity.IsZero())
}

func TestBuildManifestIncludesLocalUpstreamAheadBehind(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(remote, 0755))
	runGit(t, remote, "init", "--bare")
	repo := initFleetTestRepo(t, remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "checkout", "-b", "feature/upstream")
	runGit(t, repo, "push", "-u", "origin", "feature/upstream")
	runGit(t, repo, "checkout", "main")
	wtPath := filepath.Join(t.TempDir(), "feature-upstream")
	runGit(t, repo, "worktree", "add", wtPath, "feature/upstream")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("local commit\n"), 0644))
	runGit(t, wtPath, "add", ".")
	runGit(t, wtPath, "commit", "-m", "Local commit")

	manifest, err := buildTestManifestForRepo(t, repo)
	require.NoError(t, err)
	feature := findBranch(t, manifest, "feature/upstream")
	assert.Equal(t, "origin/feature/upstream", feature.Upstream)
	assert.Equal(t, 1, feature.Ahead)
	assert.Equal(t, 0, feature.Behind)
	assert.Equal(t, ChangeStatus{}, feature.Status)
}

func TestBuildManifestUsesConfiguredProjectIdentityOverRemote(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/fork/kwt.git")
	wtPath := filepath.Join(t.TempDir(), "feature-fleet")
	runGit(t, repo, "worktree", "add", "-b", "feature/fleet", wtPath)

	cfg := &models.Config{Fleet: models.FleetConfig{HostID: "host-a"}, Projects: []models.Project{{
		Repository: "github.com/kenn-io/kwt",
		Name:       "kwt",
		Path:       repo,
	}}}
	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), cfg)

	require.NoError(t, err)
	require.Len(t, manifest.Projects, 1)
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
	assert.Equal(t, "https://github.com/fork/kwt.git", manifest.Projects[0].RemoteURL)
	for _, wt := range manifest.Worktrees {
		assert.Equal(t, "github.com/kenn-io/kwt", wt.ProjectIdentity)
	}
}

func TestBuildManifestReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(ctx, &models.Config{Fleet: models.FleetConfig{HostID: "host-a"}})

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Nil(t, manifest)
}

func TestBuildManifestPropagatesCanceledProjectWorktreeListing(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
		ListProjectWorktrees: func(context.Context, models.Project) ([]models.Worktree, error) {
			return nil, context.Canceled
		},
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "github.com/kenn-io/kwt",
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Nil(t, manifest)
}

func TestBuildManifestUsesRemoteWhenConfiguredProjectIdentityIsPathBacked(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: repo,
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.NoError(t, err)
	require.Len(t, manifest.Projects, 1)
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
	assert.Equal(t, "https://github.com/kenn-io/kwt.git", manifest.Projects[0].RemoteURL)
	assert.Equal(t, "github.com/kenn-io/kwt", findBranch(t, manifest, "main").ProjectIdentity)
}

func TestBuildManifestUsesRemoteWhenConfiguredProjectIdentityIsBarePathLike(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "workspace/org/repo",
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.NoError(t, err)
	require.Len(t, manifest.Projects, 1)
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
}

func TestBuildManifestUsesRemoteWhenConfiguredProjectIdentityIsTildePath(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "~/.config/kwt",
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.NoError(t, err)
	require.Len(t, manifest.Projects, 1)
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
}

func TestBuildManifestUsesRemoteWhenConfiguredProjectIdentityIsFileURL(t *testing.T) {
	repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "file:///tmp/org/repo",
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.NoError(t, err)
	require.Len(t, manifest.Projects, 1)
	assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
}

func TestBuildManifestSkipsProjectWhenConfiguredAndRemoteIdentitiesAreUnsupported(t *testing.T) {
	repo := initFleetTestRepo(t, "workspace/org/repo")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: repo,
			Name:       "kwt",
			Path:       repo,
		}},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestSkipsConfiguredProjectWithoutStableIdentity(t *testing.T) {
	repo := initLocalOnlyFleetTestRepo(t)

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Name: "local-only",
			Path: repo,
		}},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestSkipsConfiguredProjectWithPathLikeRemote(t *testing.T) {
	repo := initFleetTestRepo(t, "workspace/org/repo")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Name: "local-remote",
			Path: repo,
		}},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestSkipsConfiguredProjectWithFileURLRemote(t *testing.T) {
	repo := initFleetTestRepo(t, "file:///tmp/org/repo")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Name: "file-remote",
			Path: repo,
		}},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestIncludesGlobalBaseDirWorktrees(t *testing.T) {
	baseDir := t.TempDir()
	repo := initFleetTestRepoAt(t, filepath.Join(baseDir, "github.com", "kenn-io", "kwt", "main"), "https://github.com/kenn-io/kwt.git")
	runGit(t, repo, "worktree", "add", "-b", "feature/global", filepath.Join(baseDir, "github.com", "kenn-io", "kwt", "feature-global"))

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet:    models.FleetConfig{HostID: "host-a"},
		Worktree: models.WorktreeConfig{BaseDir: baseDir},
	})

	require.NoError(t, err)
	assert.Contains(t, worktreeRefs(manifest), "branch:feature/global")
	assert.Equal(t, "github.com/kenn-io/kwt", findBranch(t, manifest, "feature/global").ProjectIdentity)
}

func TestBuildManifestSkipsGlobalWorktreeWithoutStableRemoteIdentity(t *testing.T) {
	baseDir := t.TempDir()
	initLocalOnlyFleetTestRepoAt(t, filepath.Join(baseDir, "local", "kwt", "main"))

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet:    models.FleetConfig{HostID: "host-a"},
		Worktree: models.WorktreeConfig{BaseDir: baseDir},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestSkipsGlobalWorktreeWithFileURLRemote(t *testing.T) {
	baseDir := t.TempDir()
	initFleetTestRepoAt(t, filepath.Join(baseDir, "local", "kwt", "main"), "file:///tmp/org/repo")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet:    models.FleetConfig{HostID: "host-a"},
		Worktree: models.WorktreeConfig{BaseDir: baseDir},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func TestBuildManifestSkipsGlobalWorktreeWithPathLikeRemote(t *testing.T) {
	baseDir := t.TempDir()
	initFleetTestRepoAt(t, filepath.Join(baseDir, "local", "kwt", "main"), "workspace/org/repo")

	manifest, err := NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet:    models.FleetConfig{HostID: "host-a"},
		Worktree: models.WorktreeConfig{BaseDir: baseDir},
	})

	require.NoError(t, err)
	assert.Empty(t, manifest.Projects)
	assert.Empty(t, manifest.Worktrees)
}

func buildTestManifestForRepo(t *testing.T, repo string) (*Manifest, error) {
	t.Helper()
	return NewManifestBuilder(ManifestBuilderOptions{
		Now:      func() time.Time { return fixedTime },
		Hostname: func() (string, error) { return "Host-A", nil },
	}).Build(context.Background(), &models.Config{
		Fleet: models.FleetConfig{HostID: "host-a"},
		Projects: []models.Project{{
			Repository: "github.com/kenn-io/kwt",
			Name:       "kwt",
			Path:       repo,
		}},
	})
}

func initFleetTestRepo(t *testing.T, remoteURL string) string {
	t.Helper()
	repo := t.TempDir()
	return initFleetTestRepoAt(t, repo, remoteURL)
}

func initFleetTestRepoAt(t *testing.T, repo, remoteURL string) string {
	t.Helper()
	repo = initLocalOnlyFleetTestRepoAt(t, repo)
	runGit(t, repo, "remote", "add", "origin", remoteURL)
	return repo
}

func initLocalOnlyFleetTestRepo(t *testing.T) string {
	t.Helper()
	return initLocalOnlyFleetTestRepoAt(t, t.TempDir())
}

func initLocalOnlyFleetTestRepoAt(t *testing.T, repo string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(repo, 0755))
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test Repository\n"), 0644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "Initial commit")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(output))
	return string(output)
}

func worktreeRefs(manifest *Manifest) []string {
	refs := make([]string, 0, len(manifest.Worktrees))
	for _, wt := range manifest.Worktrees {
		refs = append(refs, wt.Kind+":"+wt.Ref)
	}
	return refs
}

func findKind(t *testing.T, manifest *Manifest, kind string) WorktreeManifest {
	t.Helper()
	for _, wt := range manifest.Worktrees {
		if wt.Kind == kind {
			return wt
		}
	}
	require.Failf(t, "worktree not found", "kind %q not found in %#v", kind, manifest.Worktrees)
	return WorktreeManifest{}
}

func findBranch(t *testing.T, manifest *Manifest, branch string) WorktreeManifest {
	t.Helper()
	for _, wt := range manifest.Worktrees {
		if wt.Branch == branch {
			return wt
		}
	}
	require.Failf(t, "worktree not found", "branch %q not found in %#v", branch, manifest.Worktrees)
	return WorktreeManifest{}
}
