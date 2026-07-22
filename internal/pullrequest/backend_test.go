package pullrequest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	managedworktree "go.kenn.io/kit/git/managed"
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
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), output)
	return strings.TrimSpace(string(output))
}

func newBackendRepo(t *testing.T) (string, *GitBackend) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{
			BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true,
		},
		Projects: []models.Project{{
			Repository: testProject().Identity, Name: testProject().Name, Path: repo,
		}},
	}
	return repo, NewGitBackend(g, worktree.New(g, cfg), testProject())
}

func TestGitBackendDelegatesPullRequestLifecycleToKit(t *testing.T) {
	repo, backend := newBackendRepo(t)
	var got managedworktree.MergeRequestWorktreeOptions
	original := createMergeRequestWorktree
	createMergeRequestWorktree = func(
		_ context.Context, opts managedworktree.MergeRequestWorktreeOptions,
	) (managedworktree.CreateWorktreeResult, error) {
		got = opts
		return managedworktree.CreateWorktreeResult{
			Path: opts.Path, Branch: opts.Branch, BranchCreated: true,
		}, nil
	}
	t.Cleanup(func() { createMergeRequestWorktree = original })
	pr := testPR(17, true)

	workspace, err := backend.ImportPullRequest(
		context.Background(), pr, "pr-17-feature-widgets",
	)

	require.NoError(t, err)
	resolvedRepo, resolveErr := filepath.EvalSymlinks(repo)
	require.NoError(t, resolveErr)
	assert.Equal(t, resolvedRepo, got.ProjectRoot)
	assert.Equal(t, "pr-17-feature-widgets", got.Branch)
	assert.Equal(t, 17, got.Number)
	assert.Equal(t, pr.Source.Name, got.HeadBranch)
	assert.Equal(t, pr.Source.Repository.CloneURL, got.HeadRepoCloneURL)
	assert.Equal(t, pr.HeadSHA, got.ExpectedHeadSHA)
	assert.Equal(t, "github", got.Platform)
	assert.Equal(t, testProject().Identity, got.ProjectRepoIdentity)
	assert.Equal(t, "KWT", got.HookEnvironmentPrefix)
	assert.Contains(t, filepath.ToSlash(got.Path), "github.com/acme/widget/pr-17-feature-widgets")
	assert.Equal(t, got.Path, workspace.Path)
	assert.Equal(t, "pr-17-feature-widgets", workspace.Branch)
	assert.NotEmpty(t, workspace.ID)
	assert.NotEmpty(t, workspace.SessionName)
}

func TestGitBackendMapsSharedLifecycleErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code ErrorCode
	}{
		{name: "authentication", err: &managedworktree.ChangeRequestError{
			Kind: managedworktree.ChangeRequestAuthentication, Message: "authentication failed",
		}, code: CodeAuthentication},
		{name: "network", err: &managedworktree.ChangeRequestError{
			Kind: managedworktree.ChangeRequestNetwork, Message: "network failed",
		}, code: CodeNetwork},
		{name: "head", err: &managedworktree.ChangeRequestError{
			Kind: managedworktree.ChangeRequestInaccessibleHead, Message: "head missing",
		}, code: CodeInaccessibleHead},
		{name: "changed", err: &managedworktree.ChangeRequestError{
			Kind: managedworktree.ChangeRequestHeadChanged, Message: "head changed",
		}, code: CodeConflict},
		{name: "git", err: &managedworktree.ChangeRequestError{
			Kind: managedworktree.ChangeRequestUnsupportedGit, Message: "git unsupported",
		}, code: CodeUnsupportedGitVersion},
		{name: "branch", err: managedworktree.ErrBranchInUse, code: CodeNamingConflict},
		{name: "path", err: managedworktree.ErrWorktreeDestinationExists, code: CodeNamingConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapSharedChangeRequestError(tc.err)
			var typed *Error
			require.ErrorAs(t, mapped, &typed)
			assert.Equal(t, tc.code, typed.Code)
		})
	}
}

func TestSafeGitEnvironmentRemovesKWTSecrets(t *testing.T) {
	environment := []string{
		"PATH=/bin", "KWT_GITHUB_TOKEN=secret", "KWT_FLEET_TOKEN=fleet",
		"KWT_HOME=/private/kwt", "VISIBLE=yes",
	}

	got := SafeGitEnvironment(environment)

	assert.Equal(t, []string{"PATH=/bin", "VISIBLE=yes"}, got)
}

func TestGitBackendListWorkspacesOmitsMissingRegistrations(t *testing.T) {
	repo, backend := newBackendRepo(t)
	path := filepath.Join(t.TempDir(), "missing")
	runGit(t, repo, "worktree", "add", "-b", "missing-worktree", path)
	require.NoError(t, os.RemoveAll(path))

	workspaces, err := backend.ListWorkspaces(context.Background())

	require.NoError(t, err)
	for _, candidate := range workspaces {
		assert.NotEqual(t, path, candidate.Path)
	}
}

func TestMapSharedChangeRequestErrorPreservesUnknownErrors(t *testing.T) {
	want := errors.New("application failure")
	assert.ErrorIs(t, mapSharedChangeRequestError(want), want)
}
