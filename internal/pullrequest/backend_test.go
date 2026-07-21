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

func newBackendRepo(t *testing.T, options ...GitBackendOption) (string, *GitBackend) {
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
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject(), options...)
	return repo, backend
}

func installReferenceTransactionLeakHook(t *testing.T, repo, leakPath string) {
	t.Helper()
	quotedLeakPath := "'" + strings.ReplaceAll(filepath.ToSlash(leakPath), "'", "'\\''") + "'"
	script := "#!/bin/sh\nprintf '%s|%s|%s' \"$KWT_GITHUB_TOKEN\" \"$KWT_FLEET_TOKEN\" \"$CUSTOM_FLEET_TOKEN\" > " + quotedLeakPath + "\n"
	hookPath := filepath.Join(repo, ".git", "hooks", "reference-transaction")
	require.NoError(t, os.WriteFile(hookPath, []byte(script), 0o755))
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

func TestGitBackendDoesNotReuseRemoteWithAdditionalPushURL(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "personal", "https://github.com/octocat/widget.git")
	runGit(t, repo, "remote", "set-url", "--add", "--push", "personal", "https://github.com/octocat/widget.git")
	runGit(t, repo, "remote", "set-url", "--add", "--push", "personal", "https://github.com/attacker/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
}

func TestGitBackendDoesNotReuseRemoteWithDuplicateMatchingPushURLs(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "personal", "https://github.com/octocat/widget.git")
	runGit(t, repo, "remote", "set-url", "--add", "--push", "personal", "https://github.com/octocat/widget.git")
	runGit(t, repo, "remote", "set-url", "--add", "--push", "personal", "https://github.com/octocat/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
}

func TestGitBackendDoesNotReuseRemoteWithCustomPushRefspec(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "personal", "https://github.com/octocat/widget.git")
	runGit(t, repo, "config", "remote.personal.push", "HEAD:refs/heads/not-the-pr")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
}

func TestGitBackendDoesNotReuseCredentialBearingEffectiveRemote(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "personal", "https://oauth2:secret@github.com/octocat/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
}

func TestGitBackendRemovesNewRemoteWhenEffectiveURLsAreRewritten(t *testing.T) {
	for _, rewrite := range []string{"insteadOf", "pushInsteadOf"} {
		t.Run(rewrite, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
			runGit(t, repo, "config", "url.https://github.com/attacker/."+rewrite, "https://github.com/octocat/")

			_, err := backend.EnsureRemote(context.Background(), Repository{
				Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
				Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
			})

			assertErrorCode(t, err, CodeWorkspaceCreation)
			assert.NotContains(t, strings.Fields(runGit(t, repo, "remote")), "kwt-pr-octocat")
		})
	}
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

func TestGitBackendFetchDisablesHooksAndSanitizesEnvironment(t *testing.T) {
	t.Setenv("KWT_GITHUB_TOKEN", "github-secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet-secret")
	t.Setenv("CUSTOM_FLEET_TOKEN", "custom-secret")
	repo, backend := newBackendRepo(t, WithFleetTokenEnvironment("CUSTOM_FLEET_TOKEN"))
	bare := filepath.Join(t.TempDir(), "fork.git")
	runGit(t, repo, "init", "--bare", bare)
	runGit(t, repo, "push", bare, "HEAD:refs/heads/topic")
	runGit(t, repo, "remote", "add", "fork", bare)
	leakPath := filepath.Join(t.TempDir(), "fetch-hook-env")
	installReferenceTransactionLeakHook(t, repo, leakPath)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/hook-probe", head)
	if _, statErr := os.Stat(leakPath); os.IsNotExist(statErr) {
		t.Skip("installed Git does not support reference-transaction hooks")
	}
	require.NoError(t, os.Remove(leakPath))

	_, err := backend.Fetch(context.Background(), "fork", "refs/heads/topic", "refs/kwt/test")

	require.NoError(t, err)
	assert.NoFileExists(t, leakPath)
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

func TestGitBackendValidateImportRejectsCredentialBearingRemoteURLs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pushURL bool
	}{
		{name: "fetch URL"},
		{name: "push URL", pushURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
			credentialURL := "https://oauth2:never-log-this-secret@github.com/acme/widget.git"
			if tc.pushURL {
				runGit(t, repo, "remote", "set-url", "--push", "origin", credentialURL)
			} else {
				runGit(t, repo, "remote", "set-url", "origin", credentialURL)
			}

			err := backend.ValidateImport(context.Background())

			assertErrorCode(t, err, CodeAuthentication)
			assert.NotContains(t, err.Error(), "never-log-this-secret")
			assert.NotContains(t, err.Error(), "oauth2")
		})
	}
}

func TestGitBackendValidateImportRejectsCredentialsFromIncludedConfig(t *testing.T) {
	repo, backend := newBackendRepo(t)
	includePath := filepath.Join(t.TempDir(), "included.gitconfig")
	credentialURL := "https://oauth2:included-secret@github.com/acme/widget.git"
	require.NoError(t, os.WriteFile(includePath, []byte("[remote \"included\"]\n\turl = "+credentialURL+"\n"), 0o600))
	runGit(t, repo, "config", "include.path", includePath)

	err := backend.ValidateImport(context.Background())

	assertErrorCode(t, err, CodeAuthentication)
	assert.NotContains(t, err.Error(), "included-secret")
}

func TestGitBackendValidateImportRejectsCredentialsFromURLRewrite(t *testing.T) {
	for _, rewrite := range []string{"insteadOf", "pushInsteadOf"} {
		t.Run(rewrite, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", "https://alias/acme/widget.git")
			runGit(t, repo, "config", "url.https://oauth2:rewrite-secret@github.com/."+rewrite, "https://alias/")

			err := backend.ValidateImport(context.Background())

			assertErrorCode(t, err, CodeAuthentication)
			assert.NotContains(t, err.Error(), "rewrite-secret")
		})
	}
}

func TestRemoteURLCredentialDetectionAllowsAgentBasedSSH(t *testing.T) {
	for _, tc := range []struct {
		remoteURL string
		want      bool
	}{
		{remoteURL: "https://github.com/acme/widget.git"},
		{remoteURL: "https://token@github.com/acme/widget.git", want: true},
		{remoteURL: "git@github.com:acme/widget.git"},
		{remoteURL: "ssh://git@github.com/acme/widget.git"},
		{remoteURL: "ssh://git:secret@github.com/acme/widget.git", want: true},
		{remoteURL: "git:secret@github.com:acme/widget.git", want: true},
	} {
		assert.Equal(t, tc.want, remoteURLHasEmbeddedCredentials(tc.remoteURL), tc.remoteURL)
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

func TestPRImportCreatePropagatesStrictSetupFailure(t *testing.T) {
	repo := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: resolvedRepo}},
		RepositorySettings: []models.RepositorySetting{{
			Repository: resolvedRepo, SetupCommands: []string{"exit 17"},
		}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/10", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())

	workspace, err := backend.Create(context.Background(), "pr-10-setup-fails", "refs/kwt/pull-requests/acme/widget/10")
	if workspace.Path != "" {
		t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	}

	require.Error(t, err)
	assert.NotEmpty(t, workspace.Path, "service needs the created workspace for rollback")
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

func TestGitBackendCreatesForkRemoteUsingProjectSSHTransport(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "origin", "git@github.com:acme/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
		SSHURL: "git@github.com:octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
	assert.Equal(t, "git@github.com:octocat/widget.git", runGit(t, repo, "remote", "get-url", remote))
}

func TestGitBackendCreatesForkRemoteUsingProjectPushTransport(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
	runGit(t, repo, "remote", "set-url", "--push", "origin", "git@github.com:acme/widget.git")

	remote, err := backend.EnsureRemote(context.Background(), Repository{
		Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
		Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
		SSHURL: "git@github.com:octocat/widget.git",
	})

	require.NoError(t, err)
	assert.Equal(t, "kwt-pr-octocat", remote)
	assert.Equal(t, "git@github.com:octocat/widget.git", runGit(t, repo, "remote", "get-url", remote))
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
	wrong := filepath.Join(t.TempDir(), "wrong.git")
	runGit(t, repo, "init", "--bare", bare)
	runGit(t, repo, "init", "--bare", wrong)
	runGit(t, repo, "remote", "add", "fork", bare)
	runGit(t, repo, "remote", "add", "wrong", wrong)
	runGit(t, repo, "config", "remote.pushDefault", "wrong")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "push", "fork", fmt.Sprintf("%s:refs/heads/feature/widgets", head))
	runGit(t, repo, "config", "remote.fork.mirror", "true")
	runGit(t, repo, "config", "push.followTags", "true")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/8", head)
	workspace, err := backend.Create(context.Background(), "pr-8-feature-widgets", "refs/kwt/pull-requests/acme/widget/8")
	require.NoError(t, err)
	runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.pushRemote", "wrong")

	require.NoError(t, backend.ConfigurePush(context.Background(), workspace, "fork", "feature/widgets"))

	assert.Equal(t, "fork", runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.remote"))
	assert.Equal(t, "fork", runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.pushRemote"))
	assert.Equal(t, "refs/heads/feature/widgets", runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.merge"))
	assert.Equal(t, "HEAD:refs/heads/feature/widgets", runGit(t, workspace.Path, "config", "--worktree", "remote.fork.push"))
	assert.Equal(t, "false", runGit(t, workspace.Path, "config", "--worktree", "remote.fork.mirror"))
	assert.Equal(t, "false", runGit(t, workspace.Path, "config", "--worktree", "push.followTags"))
	assert.Equal(t, "upstream", runGit(t, workspace.Path, "config", "--worktree", "push.default"))
	for _, key := range []string{"branch.pr-8-feature-widgets.remote", "branch.pr-8-feature-widgets.merge"} {
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = repo
		output, configErr := cmd.CombinedOutput()
		assert.Error(t, configErr, "%s unexpectedly visible in main checkout: %s", key, output)
	}
	assert.Equal(t, "wrong", runGit(t, repo, "config", "branch.pr-8-feature-widgets.pushRemote"))
	cmd := exec.Command("git", "config", "--get", "remote.fork.push")
	cmd.Dir = repo
	output, configErr := cmd.CombinedOutput()
	assert.Error(t, configErr, "remote.fork.push unexpectedly visible in main checkout: %s", output)
	require.NoError(t, os.WriteFile(filepath.Join(workspace.Path, "change.txt"), []byte("change\n"), 0644))
	runGit(t, workspace.Path, "add", "change.txt")
	runGit(t, workspace.Path, "commit", "-m", "change")
	pushOutput := runGit(t, workspace.Path, "push", "--dry-run")
	assert.Contains(t, pushOutput, "HEAD -> feature/widgets")
}

func TestGitBackendRollbackDisablesReferenceTransactionHooks(t *testing.T) {
	t.Setenv("KWT_GITHUB_TOKEN", "github-secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet-secret")
	t.Setenv("CUSTOM_FLEET_TOKEN", "custom-secret")
	repo, backend := newBackendRepo(t, WithFleetTokenEnvironment("CUSTOM_FLEET_TOKEN"))
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/81", head)
	workspace, err := backend.Create(context.Background(), "pr-81-rollback", "refs/kwt/pull-requests/acme/widget/81")
	require.NoError(t, err)
	leakPath := filepath.Join(t.TempDir(), "rollback-hook-env")
	installReferenceTransactionLeakHook(t, repo, leakPath)

	err = backend.Rollback(context.Background(), workspace)

	require.NoError(t, err)
	assert.NoFileExists(t, leakPath)
}
