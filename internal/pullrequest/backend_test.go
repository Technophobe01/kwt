package pullrequest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestGitBackendMatchesGitHubRemoteCaseInsensitively(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://github.com/Acme/Widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/acme/widget", Host: "github.com",
		Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "origin", remote)
}

func TestGitBackendFetchClassifiesHTTPAuthenticationFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "private", server.URL+"/repo.git")

			_, err := backend.Fetch(context.Background(), "private", "refs/heads/topic", "refs/kwt/test")

			assertErrorCode(t, err, CodeAuthentication)
		})
	}
}

func TestParseGitVersionRequiresWorktreeConfigSupport(t *testing.T) {
	for _, tc := range []struct {
		output string
		ok     bool
	}{
		{output: "git version 2.19.6", ok: false},
		{output: "git version 2.20.0", ok: true},
		{output: "git version 2.45.2 (Apple Git-145)", ok: true},
		{output: "not git", ok: false},
	} {
		assert.Equal(t, tc.ok, supportsWorktreeConfig(tc.output), tc.output)
	}
}

func TestSafeSetupEnvironmentRemovesConfiguredSecrets(t *testing.T) {
	environment := []string{"PATH=/bin", "KWT_GITHUB_TOKEN=secret", "KWT_FLEET_TOKEN=fleet", "CUSTOM_TOKEN=custom", "VISIBLE=yes"}

	got := SafeSetupEnvironment(environment, "CUSTOM_TOKEN")

	assert.Equal(t, []string{"PATH=/bin", "VISIBLE=yes"}, got)
}

func TestSafeSetupEnvironmentPreservesExplicitEmptyEnvironment(t *testing.T) {
	got := SafeSetupEnvironment([]string{"kwt_github_token=secret", "Kwt_Fleet_Token=fleet", "custom_token=custom"}, "CUSTOM_TOKEN")

	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestNewGitBackendSanitizesKWTSecretsByDefault(t *testing.T) {
	t.Setenv("KWT_GITHUB_TOKEN", "secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet")
	repo, backend := newBackendRepo(t)
	_ = repo

	assert.NotContains(t, backend.setupEnvironment, "KWT_GITHUB_TOKEN=secret")
	assert.NotContains(t, backend.setupEnvironment, "KWT_FLEET_TOKEN=fleet")
	assert.NotNil(t, backend.setupEnvironment)
}

func TestPRImportSetupCommandsDoNotReceiveKWTSecrets(t *testing.T) {
	repo := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: resolvedRepo}},
		Fleet:    models.FleetConfig{TokenEnv: "CUSTOM_FLEET_SECRET"},
		RepositorySettings: []models.RepositorySetting{{
			Repository:    resolvedRepo,
			SetupCommands: []string{"printf '%s|%s|%s|%s' \"$KWT_GITHUB_TOKEN\" \"$KWT_FLEET_TOKEN\" \"$CUSTOM_FLEET_SECRET\" \"$VISIBLE_VALUE\" > setup-env.txt"},
		}},
	}
	t.Setenv("KWT_GITHUB_TOKEN", "github-secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet-secret")
	t.Setenv("CUSTOM_FLEET_SECRET", "custom-secret")
	t.Setenv("VISIBLE_VALUE", "visible")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/9", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject(), WithFleetTokenEnvironment(cfg.Fleet.TokenEnv))

	workspace, err := backend.Create(context.Background(), "pr-9-safe-env", "refs/kwt/pull-requests/acme/widget/9")

	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(workspace.Path, "setup-env.txt"))
	require.NoError(t, err)
	assert.Equal(t, "|||visible", string(contents))
}

func TestPRCheckoutDisablesHooksAndSanitizesFilterEnvironment(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("filtered.txt filter=kwt-capture\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "filtered.txt"), []byte("content\n"), 0o644))
	runGit(t, repo, "add", ".gitattributes", "filtered.txt")
	runGit(t, repo, "commit", "-m", "add filtered content")

	filterScript := filepath.Join(t.TempDir(), "filter.sh")
	require.NoError(t, os.WriteFile(filterScript, []byte("#!/bin/sh\nif test -n \"$KWT_GITHUB_TOKEN$KWT_FLEET_TOKEN$CUSTOM_FLEET_TOKEN\"; then exit 42; fi\nprintf 'filtered:'\ncat\n"), 0o755))
	filterCommand := "sh '" + strings.ReplaceAll(filepath.ToSlash(filterScript), "'", "'\\''") + "'"
	runGit(t, repo, "config", "filter.kwt-capture.smudge", filterCommand)
	hookPath := filepath.Join(repo, ".git", "hooks", "post-checkout")
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 73\n"), 0o755))

	t.Setenv("KWT_GITHUB_TOKEN", "github-secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet-secret")
	t.Setenv("CUSTOM_FLEET_TOKEN", "custom-secret")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Fleet:    models.FleetConfig{TokenEnv: "CUSTOM_FLEET_TOKEN"},
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: repo}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/10", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject(), WithFleetTokenEnvironment(cfg.Fleet.TokenEnv))

	workspace, err := backend.Create(context.Background(), "pr-10-safe-checkout", "refs/kwt/pull-requests/acme/widget/10")

	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(workspace.Path, "filtered.txt"))
	require.NoError(t, err)
	assert.Equal(t, "filtered:content\n", string(contents))
}

func TestGitBackendListWorkspacesOmitsMissingWorktrees(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/11", head)
	workspace, err := backend.Create(context.Background(), "pr-11-stale", "refs/kwt/pull-requests/acme/widget/11")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(workspace.Path))

	workspaces, err := backend.ListWorkspaces(context.Background())

	require.NoError(t, err)
	for _, candidate := range workspaces {
		assert.NotEqual(t, workspace.Path, candidate.Path)
	}
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
	for _, key := range []string{"branch.pr-8-feature-widgets.remote", "branch.pr-8-feature-widgets.merge"} {
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = repo
		output, configErr := cmd.CombinedOutput()
		assert.Error(t, configErr, "%s unexpectedly visible in main checkout: %s", key, output)
	}
	require.NoError(t, os.WriteFile(filepath.Join(workspace.Path, "change.txt"), []byte("change\n"), 0644))
	runGit(t, workspace.Path, "add", "change.txt")
	runGit(t, workspace.Path, "commit", "-m", "change")
	output := runGit(t, workspace.Path, "push", "--dry-run")
	assert.Contains(t, output, "pr-8-feature-widgets -> feature/widgets")
}
