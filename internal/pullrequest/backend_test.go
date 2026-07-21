package pullrequest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitadapter "go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/worktree"
	"go.kenn.io/kwt/pkg/models"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), output)
	return strings.TrimSpace(string(output))
}

func newBackendRepo(t *testing.T) (string, *GitBackend) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: repo}},
	}
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())
	return repo, backend
}

func TestGitBackendSelectsMatchingRemoteAmongMultipleRemotes(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
	runGit(t, repo, "remote", "add", "personal", "git@github.com:octocat/widget.git")
	runGit(t, repo, "remote", "add", "mirror", "https://github.com/mirror/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "personal", remote)
}

func TestGitBackendCreatesDeterministicRemoteWithoutOverwritingCollision(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "kwt-pr-octocat", "https://github.com/someone/other.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat-2", remote)
	assert.Equal(t, "https://github.com/octocat/widget.git", runGit(t, repo, "remote", "get-url", remote))
}

func TestGitBackendFetchReportsUnavailableHead(t *testing.T) {
	repo, backend := newBackendRepo(t)
	bare := filepath.Join(t.TempDir(), "fork.git")
	runGit(t, repo, "init", "--bare", bare)
	runGit(t, repo, "remote", "add", "fork", bare)

	_, err := backend.Fetch(context.Background(), "fork", "refs/heads/deleted", "refs/kwt/pull-requests/acme/widget/1")

	assertErrorCode(t, err, CodeInaccessibleHead)
}

func TestGitBackendCreateUsesCanonicalWorktreeManagerPath(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/7", head)

	workspace, err := backend.Create(context.Background(), "pr-7-feature", "refs/kwt/pull-requests/acme/widget/7")

	require.NoError(t, err)
	assert.DirExists(t, workspace.Path)
	assert.Equal(t, "pr-7-feature", workspace.Branch)
	assert.Equal(t, testProject().Identity, workspace.Repository)
	assert.Contains(t, filepath.ToSlash(workspace.Path), "github.com/acme/widget/pr-7-feature")
	assert.NotEmpty(t, workspace.ID)
	assert.NotEmpty(t, workspace.SessionName)
}

func TestGitBackendCreateClassifiesDuplicateBranchOrWorkspaceName(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/70", head)
	_, err := backend.Create(context.Background(), "pr-70-feature", "refs/kwt/pull-requests/acme/widget/70")
	require.NoError(t, err)

	_, err = backend.Create(context.Background(), "pr-70-feature", "refs/kwt/pull-requests/acme/widget/70")

	assertErrorCode(t, err, CodeNamingConflict)
}

func TestGitBackendConfiguresPlainPushToOriginalHeadBranch(t *testing.T) {
	repo, backend := newBackendRepo(t)
	bare := filepath.Join(t.TempDir(), "fork.git")
	runGit(t, repo, "init", "--bare", bare)
	runGit(t, repo, "remote", "add", "fork", bare)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "push", "fork", fmt.Sprintf("%s:refs/heads/feature/widgets", head))
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/8", head)
	workspace, err := backend.Create(context.Background(), "pr-8-feature-widgets", "refs/kwt/pull-requests/acme/widget/8")
	require.NoError(t, err)

	require.NoError(t, backend.ConfigurePush(context.Background(), workspace, "fork", "feature/widgets"))

	assert.Equal(t, "fork", runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.remote"))
	assert.Equal(t, "refs/heads/feature/widgets", runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.merge"))
	assert.Equal(t, "upstream", runGit(t, workspace.Path, "config", "--worktree", "push.default"))
	require.NoError(t, os.WriteFile(filepath.Join(workspace.Path, "change.txt"), []byte("change\n"), 0644))
	runGit(t, workspace.Path, "add", "change.txt")
	runGit(t, workspace.Path, "commit", "-m", "change")
	output := runGit(t, workspace.Path, "push", "--dry-run")
	assert.Contains(t, output, "pr-8-feature-widgets -> feature/widgets")
}
