package pullrequest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestGitBackendValidateImportRejectsQueryAndFragmentRemoteURLs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remoteURL string
		pushURL   bool
	}{
		{name: "fetch query", remoteURL: "https://github.com/acme/widget.git?access_token=never-log-query"},
		{name: "push fragment", remoteURL: "https://github.com/acme/widget.git#never-log-fragment", pushURL: true},
		{name: "malformed scheme URL", remoteURL: "https://github.com/%zz", pushURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
			args := []string{"remote", "set-url"}
			if tc.pushURL {
				args = append(args, "--push")
			}
			runGit(t, repo, append(args, "origin", tc.remoteURL)...)

			err := backend.ValidateImport(context.Background())

			assertErrorCode(t, err, CodeAuthentication)
			assert.NotContains(t, err.Error(), "never-log")
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

func TestGitBackendValidateImportRejectsQueryCredentialsFromURLRewrite(t *testing.T) {
	for _, rewrite := range []string{"insteadOf", "pushInsteadOf"} {
		t.Run(rewrite, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", "https://alias/acme/widget.git")
			runGit(t, repo, "config", "url.https://github.com/?access_token=rewrite-secret."+rewrite, "https://alias/")

			err := backend.ValidateImport(context.Background())

			assertErrorCode(t, err, CodeAuthentication)
			assert.NotContains(t, err.Error(), "rewrite-secret")
		})
	}
}

func TestGitBackendValidateImportRejectsRemoteHelperURLs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remoteURL string
		rewrite   bool
	}{
		{name: "direct", remoteURL: "corp::--token=never-log-helper"},
		{name: "rewrite", remoteURL: "corp::--token=never-log-helper", rewrite: true},
		{name: "empty transport direct", remoteURL: "::--token=never-log-helper"},
		{name: "empty transport rewrite", remoteURL: "::--token=never-log-helper", rewrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			if tc.rewrite {
				runGit(t, repo, "remote", "add", "origin", "https://alias/acme/widget.git")
				runGit(t, repo, "config", "url."+tc.remoteURL+".insteadOf", "https://alias/")
			} else {
				runGit(t, repo, "remote", "add", "origin", tc.remoteURL)
			}

			err := backend.ValidateImport(context.Background())

			assertErrorCode(t, err, CodeAuthentication)
			assert.NotContains(t, err.Error(), "never-log-helper")
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
		{remoteURL: "https://github.com/acme/widget.git?access_token=secret", want: true},
		{remoteURL: "https://github.com/acme/widget.git#token", want: true},
		{remoteURL: "https://github.com/%zz", want: true},
		{remoteURL: "git@github.com:acme/widget.git?access_token=secret", want: true},
		{remoteURL: "corp::--token=secret", want: true},
		{remoteURL: "::--token=secret", want: true},
		{remoteURL: "ssh://git@[2001:db8::1]/acme/widget.git"},
	} {
		assert.Equal(t, tc.want, remoteURLHasEmbeddedCredentials(tc.remoteURL), tc.remoteURL)
	}
}

func TestGitBackendValidateImportRejectsCustomReceivePack(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
	runGit(t, repo, "config", "remote.origin.receivepack", "sh -c 'redirect push'")

	err := backend.ValidateImport(context.Background())

	assertErrorCode(t, err, CodeWorkspaceCreation)
	assert.NotContains(t, err.Error(), "redirect push")
}

func TestSafeSetupEnvironmentRemovesConfiguredSecrets(t *testing.T) {
	environment := []string{
		"PATH=/bin", "KWT_GITHUB_TOKEN=secret", "KWT_FLEET_TOKEN=fleet",
		"KWT_HOME=/private/kwt", "KWT_SHELL_DEPTH=1", "CUSTOM_TOKEN=custom", "VISIBLE=yes",
	}

	got := SafeSetupEnvironment(environment, "CUSTOM_TOKEN", "KWT_FLEET_TOKEN_FILE")

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

func TestPRImportSkipsRepositorySetupCommands(t *testing.T) {
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
		Fleet: models.FleetConfig{
			TokenEnv:             "CUSTOM_FLEET_SECRET",
			TokenFileEnvironment: []string{"KWT_FLEET_TOKEN_FILE"},
		},
		RepositorySettings: []models.RepositorySetting{{
			Repository:    resolvedRepo,
			SetupCommands: []string{"printf '%s|%s|%s|%s|%s' \"$KWT_GITHUB_TOKEN\" \"$KWT_FLEET_TOKEN\" \"$CUSTOM_FLEET_SECRET\" \"$KWT_FLEET_TOKEN_FILE\" \"$VISIBLE_VALUE\" > setup-env.txt"},
		}},
	}
	t.Setenv("KWT_GITHUB_TOKEN", "github-secret")
	t.Setenv("KWT_FLEET_TOKEN", "fleet-secret")
	t.Setenv("CUSTOM_FLEET_SECRET", "custom-secret")
	tokenFile := filepath.Join(t.TempDir(), "fleet.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-file-secret"), 0o600))
	t.Setenv("KWT_FLEET_TOKEN_FILE", tokenFile)
	t.Setenv("VISIBLE_VALUE", "visible")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/9", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject(),
		WithFleetTokenEnvironment(cfg.Fleet.TokenEnv),
		WithFleetTokenFileEnvironment(cfg.Fleet.TokenFileEnvironment),
	)

	workspace, err := backend.Create(context.Background(), "pr-9-safe-env", "refs/kwt/pull-requests/acme/widget/9")

	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(workspace.Path, "setup-env.txt"))
}

func TestPRImportSetupCannotDiscoverFleetTokenThroughKWTHome(t *testing.T) {
	repo := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")

	tokenFile := filepath.Join(t.TempDir(), "fleet.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("fleet-bearer-secret"), 0o600))
	kwtHome := t.TempDir()
	configText := "[fleet]\ntoken_file = \"" + filepath.ToSlash(tokenFile) + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(kwtHome, "config.toml"), []byte(configText), 0o600))
	t.Setenv("KWT_HOME", kwtHome)

	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: resolvedRepo}},
		RepositorySettings: []models.RepositorySetting{{
			Repository: resolvedRepo,
			SetupCommands: []string{
				`if test -n "$KWT_HOME"; then token_file=$(sed -n 's/^token_file = "\(.*\)"$/\1/p' "$KWT_HOME/config.toml"); cat "$token_file" > stolen-token.txt; fi`,
			},
		}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/91", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())

	workspace, err := backend.Create(context.Background(), "pr-91-safe-home", "refs/kwt/pull-requests/acme/widget/91")

	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(workspace.Path, "stolen-token.txt"))
}

func TestPRImportCreateDoesNotRunFailingSetupCommand(t *testing.T) {
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
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })

	require.NoError(t, err)
	assert.DirExists(t, workspace.Path)
}

func TestPRImportCreateDoesNotStartSlowSetupCommand(t *testing.T) {
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
			Repository: resolvedRepo, SetupCommands: []string{"sleep 3 & wait"},
		}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/12", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())
	started := time.Now()

	workspace, err := backend.Create(context.Background(), "pr-12-cancel-setup", "refs/kwt/pull-requests/acme/widget/12")
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })

	require.NoError(t, err)
	assert.Less(t, time.Since(started), 2*time.Second)
	assert.DirExists(t, workspace.Path)
}

func TestPRImportValidatesBranchConditionalConfigBeforeCheckoutAndSetup(t *testing.T) {
	repo := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/widget.git")
	conditionalPath := filepath.Join(t.TempDir(), "pr-branch.gitconfig")
	require.NoError(t, os.WriteFile(conditionalPath, []byte(
		"[remote \"origin\"]\n\turl = https://oauth2:conditional-secret@github.com/acme/widget.git\n",
	), 0o600))
	runGit(t, repo, "config", "includeIf.onbranch:pr-*.path", conditionalPath)
	marker := filepath.Join(t.TempDir(), "setup-ran")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: resolvedRepo}},
		RepositorySettings: []models.RepositorySetting{{
			Repository: resolvedRepo, SetupCommands: []string{"touch '" + marker + "'"},
		}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/13", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())
	require.NoError(t, backend.ValidateImport(context.Background()), "main-branch validation should not activate the conditional include")

	workspace, err := backend.Create(context.Background(), "pr-13-conditional", "refs/kwt/pull-requests/acme/widget/13")
	if workspace.Path != "" {
		t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	}

	assertErrorCode(t, err, CodeAuthentication)
	assert.NotContains(t, err.Error(), "conditional-secret")
	assert.NoFileExists(t, marker)
}

func TestPRImportRejectsReorderedBranchConditionalConfiguration(t *testing.T) {
	repo := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	mainConfig := filepath.Join(t.TempDir(), "main.gitconfig")
	prConfig := filepath.Join(t.TempDir(), "pr.gitconfig")
	require.NoError(t, os.WriteFile(mainConfig, []byte("[credential]\n\thelper = first\n\thelper = second\n"), 0o600))
	require.NoError(t, os.WriteFile(prConfig, []byte("[credential]\n\thelper = second\n\thelper = first\n"), 0o600))
	runGit(t, repo, "config", "includeIf.onbranch:main.path", mainConfig)
	runGit(t, repo, "config", "includeIf.onbranch:pr-*.path", prConfig)
	marker := filepath.Join(t.TempDir(), "setup-ran")
	g := gitadapter.New(repo)
	cfg := &models.Config{
		Worktree: models.WorktreeConfig{BaseDir: filepath.Join(t.TempDir(), "worktrees"), AutoMkdir: true},
		Projects: []models.Project{{Repository: testProject().Identity, Name: testProject().Name, Path: resolvedRepo}},
		RepositorySettings: []models.RepositorySetting{{
			Repository: resolvedRepo, SetupCommands: []string{"touch '" + marker + "'"},
		}},
	}
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/15", head)
	backend := NewGitBackend(g, worktree.New(g, cfg), testProject())

	workspace, err := backend.Create(context.Background(), "pr-15-order", "refs/kwt/pull-requests/acme/widget/15")
	if workspace.Path != "" {
		t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	}

	assertErrorCode(t, err, CodeAuthentication)
	assert.NoFileExists(t, marker)
}

func TestPRCheckoutAndRollbackDisableHooksAndFilters(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("filtered.txt filter=kwt-capture\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "filtered.txt"), []byte("content\n"), 0o644))
	runGit(t, repo, "add", ".gitattributes", "filtered.txt")
	runGit(t, repo, "commit", "-m", "add filtered content")

	filterMarker := filepath.Join(t.TempDir(), "filter-ran")
	filterScript := filepath.Join(t.TempDir(), "filter.sh")
	quotedFilterMarker := "'" + strings.ReplaceAll(filepath.ToSlash(filterMarker), "'", "'\\''") + "'"
	require.NoError(t, os.WriteFile(filterScript, []byte("#!/bin/sh\nprintf ran > "+quotedFilterMarker+"\nprintf 'filtered:'\ncat\n"), 0o755))
	filterCommand := "sh '" + strings.ReplaceAll(filepath.ToSlash(filterScript), "'", "'\\''") + "'"
	runGit(t, repo, "config", "filter.kwt-capture.clean", filterCommand)
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
	assert.Equal(t, "content\n", string(contents))
	assert.NoFileExists(t, filterMarker)
	require.NoError(t, backend.Rollback(context.Background(), workspace))
	assert.NoFileExists(t, filterMarker)
}

func TestGitBackendRollbackPreservesAdvancedWorkspaceAndBranch(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/93", head)
	workspace, err := backend.Create(context.Background(), "pr-93-owned", "refs/kwt/pull-requests/acme/widget/93")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace.Path, "review.txt"), []byte("keep\n"), 0o644))
	runGit(t, workspace.Path, "add", "review.txt")
	runGit(t, workspace.Path, "commit", "-m", "review work")
	advancedOID := runGit(t, workspace.Path, "rev-parse", "HEAD")

	err = backend.Rollback(context.Background(), workspace)

	require.Error(t, err)
	assert.DirExists(t, workspace.Path)
	assert.FileExists(t, filepath.Join(workspace.Path, "review.txt"))
	assert.Equal(t, advancedOID, runGit(t, repo, "rev-parse", "refs/heads/"+workspace.Branch))
}

func TestGitBackendRollbackPreservesUncommittedWorkspaceChanges(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/95", head)
	workspace, err := backend.Create(context.Background(), "pr-95-owned", "refs/kwt/pull-requests/acme/widget/95")
	require.NoError(t, err)
	marker := filepath.Join(workspace.Path, "uncommitted.txt")
	require.NoError(t, os.WriteFile(marker, []byte("keep\n"), 0o644))

	err = backend.Rollback(context.Background(), workspace)

	require.Error(t, err)
	assert.FileExists(t, marker)
	assert.Equal(t, head, runGit(t, repo, "rev-parse", "refs/heads/"+workspace.Branch))
}

func TestGitBackendRollbackPreservesReplacedWorkspacePath(t *testing.T) {
	repo, backend := newBackendRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/94", head)
	workspace, err := backend.Create(context.Background(), "pr-94-owned", "refs/kwt/pull-requests/acme/widget/94")
	require.NoError(t, err)
	originalPath := workspace.Path + "-original"
	require.NoError(t, os.Rename(workspace.Path, originalPath))
	require.NoError(t, os.Mkdir(workspace.Path, 0o755))
	marker := filepath.Join(workspace.Path, "unrelated.txt")
	require.NoError(t, os.WriteFile(marker, []byte("keep\n"), 0o644))

	err = backend.Rollback(context.Background(), workspace)

	require.Error(t, err)
	assert.FileExists(t, marker)
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

func TestGitBackendCreatesForkRemoteUsingAliasedOrPortedProjectSSHTransport(t *testing.T) {
	for _, tc := range []struct {
		projectURL string
		wantURL    string
	}{
		{projectURL: "git@workgit:acme/widget.git", wantURL: "git@workgit:octocat/widget.git"},
		{projectURL: "ssh://custom@github.com:2222/acme/widget.git", wantURL: "ssh://custom@github.com:2222/octocat/widget.git"},
	} {
		t.Run(tc.projectURL, func(t *testing.T) {
			repo, backend := newBackendRepo(t)
			runGit(t, repo, "remote", "add", "origin", tc.projectURL)

			remote, err := backend.EnsureRemote(context.Background(), Repository{
				Provider: "github", Identity: "github.com/octocat/widget", Host: "github.com",
				Owner: "octocat", Name: "widget", CloneURL: "https://github.com/octocat/widget.git",
				SSHURL: "git@github.com:octocat/widget.git",
			})

			require.NoError(t, err)
			assert.Equal(t, "kwt-pr-octocat", remote)
			assert.Equal(t, tc.wantURL, runGit(t, repo, "remote", "get-url", remote))
		})
	}
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
	wrong := filepath.Join(t.TempDir(), "wrong.git")
	runGit(t, repo, "init", "--bare", wrong)
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	runGit(t, repo, "remote", "add", "wrong", wrong)
	runGit(t, repo, "config", "remote.pushDefault", "wrong")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "config", "remote.fork.mirror", "true")
	runGit(t, repo, "config", "push.followTags", "true")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/8", head)
	workspace, err := backend.Create(context.Background(), "pr-8-feature-widgets", "refs/kwt/pull-requests/acme/widget/8")
	require.NoError(t, err)
	runGit(t, workspace.Path, "config", "branch.pr-8-feature-widgets.pushRemote", "wrong")

	require.NoError(t, backend.ConfigurePush(context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets"))

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
}

func TestGitBackendConfigurePushPersistsDisabledHooksForNormalPush(t *testing.T) {
	repo, backend := newBackendRepo(t)
	leakPath := filepath.Join(t.TempDir(), "pre-push-ran")
	hooksDir := filepath.Join(repo, ".githooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	quotedLeakPath := "'" + strings.ReplaceAll(filepath.ToSlash(leakPath), "'", "'\\''") + "'"
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-push"),
		[]byte("#!/bin/sh\nprintf ran > "+quotedLeakPath+"\n"), 0o755))
	runGit(t, repo, "add", ".githooks/pre-push")
	runGit(t, repo, "commit", "-m", "add contributor hook")
	runGit(t, repo, "config", "core.hooksPath", ".githooks")
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/92", head)
	workspace, err := backend.Create(context.Background(), "pr-92-safe-push", "refs/kwt/pull-requests/acme/widget/92")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })

	require.NoError(t, backend.ConfigurePush(
		context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets",
	))
	effectiveHooksPath := runGit(t, workspace.Path, "config", "--path", "--get", "core.hooksPath")
	assert.True(t, filepath.IsAbs(effectiveHooksPath), effectiveHooksPath)
	assert.NotEqual(t, filepath.Join(workspace.Path, ".githooks"), effectiveHooksPath)
	assert.DirExists(t, effectiveHooksPath)

	bare := filepath.Join(t.TempDir(), "fork.git")
	runGit(t, repo, "init", "--bare", bare)
	runGit(t, repo, "remote", "set-url", "fork", bare)
	runGit(t, workspace.Path, "push", "fork")

	assert.NoFileExists(t, leakPath)
}

func TestGitBackendConfigurePushRejectsConditionalRemoteRedirect(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/14", head)
	workspace, err := backend.Create(context.Background(), "pr-14-routing", "refs/kwt/pull-requests/acme/widget/14")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	conditionalPath := filepath.Join(t.TempDir(), "redirect.gitconfig")
	require.NoError(t, os.WriteFile(conditionalPath, []byte(
		"[remote \"fork\"]\n\tpushurl = https://github.com/attacker/widget.git\n",
	), 0o600))
	runGit(t, repo, "config", "includeIf.onbranch:pr-14-*.path", conditionalPath)

	err = backend.ConfigurePush(context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets")

	assertErrorCode(t, err, CodeWorkspaceCreation)
}

func TestGitBackendConfigurePushRejectsRemoteChangedAfterWorkspaceSetup(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/15", head)
	workspace, err := backend.Create(context.Background(), "pr-15-routing", "refs/kwt/pull-requests/acme/widget/15")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	runGit(t, workspace.Path, "remote", "set-url", "fork", "https://github.com/attacker/widget.git")

	err = backend.ConfigurePush(context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets")

	assertErrorCode(t, err, CodeWorkspaceCreation)
}

func TestGitBackendConfigurePushRejectsCustomReceivePackAfterSetup(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/17", head)
	workspace, err := backend.Create(context.Background(), "pr-17-routing", "refs/kwt/pull-requests/acme/widget/17")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	runGit(t, workspace.Path, "config", "remote.fork.receivepack", "sh -c 'redirect push'")

	err = backend.ConfigurePush(context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets")

	assertErrorCode(t, err, CodeWorkspaceCreation)
}

func TestGitBackendConfigurePushRejectsChangedWorkspaceHead(t *testing.T) {
	repo, backend := newBackendRepo(t)
	runGit(t, repo, "remote", "add", "fork", "https://github.com/octocat/widget.git")
	head := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/kwt/pull-requests/acme/widget/16", head)
	workspace, err := backend.Create(context.Background(), "pr-16-head", "refs/kwt/pull-requests/acme/widget/16")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Rollback(context.Background(), workspace) })
	runGit(t, workspace.Path, "checkout", "--detach", "HEAD")

	err = backend.ConfigurePush(context.Background(), workspace, "fork", "github.com/octocat/widget", "feature/widgets")

	assertErrorCode(t, err, CodeWorkspaceCreation)
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
