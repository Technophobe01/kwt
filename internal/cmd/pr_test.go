package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/pullrequest"
	"go.kenn.io/kwt/pkg/models"
)

type fakePRService struct {
	prs         []pullrequest.PullRequest
	result      pullrequest.ImportResult
	listErr     error
	importErr   error
	gotState    string
	gotSelector string
	gotProject  pullrequest.Project
}

func (f *fakePRService) List(_ context.Context, project pullrequest.Project, state string) ([]pullrequest.PullRequest, error) {
	f.gotProject = project
	f.gotState = state
	return f.prs, f.listErr
}

func (f *fakePRService) Import(_ context.Context, project pullrequest.Project, selector string) (pullrequest.ImportResult, error) {
	f.gotProject = project
	f.gotSelector = selector
	return f.result, f.importErr
}

func withPRCommandDeps(t *testing.T, cfg *models.Config, service prService) {
	t.Helper()
	oldLoad := loadPRConfig
	oldTargetLoad := loadPRTargetConfig
	oldNew := newPRService
	oldValidateRoot := validatePRProjectRoot
	oldProject := prProject
	oldState := prState
	oldStartSession := prStartSession
	oldStartWorkspaceSession := startPRWorkspaceSession
	t.Cleanup(func() {
		loadPRConfig = oldLoad
		loadPRTargetConfig = oldTargetLoad
		newPRService = oldNew
		validatePRProjectRoot = oldValidateRoot
		prProject = oldProject
		prState = oldState
		prStartSession = oldStartSession
		startPRWorkspaceSession = oldStartWorkspaceSession
	})
	loadPRConfig = func() (*models.Config, error) { return cfg, nil }
	loadPRTargetConfig = func(string, bool) (*models.Config, error) { return cfg, nil }
	newPRService = func(context.Context, *models.Config, pullrequest.Project) (prService, error) { return service, nil }
	validatePRProjectRoot = func(project pullrequest.Project) (pullrequest.Project, error) { return project, nil }
	prProject = "widget"
	prState = "open"
	prStartSession = false
}

func TestRunPRImportValidatesSelectorBeforeAuthentication(t *testing.T) {
	withPRCommandDeps(t, testPRConfig(), &fakePRService{})
	called := false
	newPRService = func(context.Context, *models.Config, pullrequest.Project) (prService, error) {
		called = true
		return nil, pullrequest.NewError(pullrequest.CodeAuthentication, "authentication required", false, nil)
	}
	cmd, stdout, _ := prTestCommand()

	err := runPRImport(cmd, []string{"invalid"})

	assertPRCode(t, err, pullrequest.CodeInvalidSelector)
	assert.False(t, called)
	var envelope pullrequest.ErrorEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
	assert.Equal(t, pullrequest.CodeInvalidSelector, envelope.Error.Code)
}

func TestPRArgumentValidationUsesStructuredErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   cobra.PositionalArgs
		args []string
	}{
		{name: "unexpected list argument", fn: prNoArgs, args: []string{"extra"}},
		{name: "missing import selector", fn: prExactArgs(1), args: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, stdout, stderr := prTestCommand()
			err := tc.fn(cmd, tc.args)
			var exitErr *prCommandError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, 2, exitErr.ExitCode())
			var envelope pullrequest.ErrorEnvelope
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
			assert.Equal(t, pullrequest.CodeInvalidSelector, envelope.Error.Code)
			assert.Contains(t, stderr.String(), "invalid_pull_request_selector")
		})
	}
}

func TestPRFlagValidationUsesStructuredErrors(t *testing.T) {
	cmd, stdout, stderr := prTestCommand()
	err := prCmd.FlagErrorFunc()(cmd, errors.New("unknown flag: --bogus"))

	var exitErr *prCommandError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 2, exitErr.ExitCode())
	var envelope pullrequest.ErrorEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
	assert.Equal(t, pullrequest.CodeInvalidSelector, envelope.Error.Code)
	assert.Contains(t, stderr.String(), "invalid_pull_request_selector")
}

func TestPreparePRServiceLoadsSelectedTargetConfiguration(t *testing.T) {
	global := testPRConfig()
	target := testPRConfig()
	target.Worktree.BaseDir = "/target/worktrees"
	withPRCommandDeps(t, global, &fakePRService{})
	var loadedPath string
	loadPRTargetConfig = func(path string, interactive bool) (*models.Config, error) {
		loadedPath = path
		assert.False(t, interactive)
		return target, nil
	}
	var received *models.Config
	newPRService = func(_ context.Context, cfg *models.Config, _ pullrequest.Project) (prService, error) {
		received = cfg
		return &fakePRService{}, nil
	}

	project, err := preparePRProject()
	require.NoError(t, err)
	_, _, err = preparePRService(context.Background(), project)

	require.NoError(t, err)
	assert.Equal(t, "/repos/widget", loadedPath)
	assert.Same(t, target, received)
}

func TestPreparePRServiceRejectsPathOutsideMainRepositoryRootBeforeLoadingConfig(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", repo)
	require.NoError(t, cmd.Run())
	subdir := filepath.Join(repo, "nested")
	require.NoError(t, os.Mkdir(subdir, 0o755))

	oldValidateRoot := validatePRProjectRoot
	oldTargetLoad := loadPRTargetConfig
	t.Cleanup(func() {
		validatePRProjectRoot = oldValidateRoot
		loadPRTargetConfig = oldTargetLoad
	})
	validatePRProjectRoot = defaultValidatePRProjectRoot
	loaded := false
	loadPRTargetConfig = func(string, bool) (*models.Config, error) {
		loaded = true
		return testPRConfig(), nil
	}

	_, _, err := preparePRService(context.Background(), pullrequest.Project{
		Identity: "github.com/acme/widget", Name: "widget", Path: subdir,
	})

	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)
	assert.False(t, loaded)
}

func prTestCommand() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	cmd := &cobra.Command{}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, stdout, stderr
}

func testPRConfig() *models.Config {
	return &models.Config{Projects: []models.Project{{
		Repository: "github.com/acme/widget", Name: "widget", Path: "/repos/widget",
	}}}
}

func TestPRCommandsSkipCallerLocalConfig(t *testing.T) {
	require.NotNil(t, prCmd.PersistentPreRunE)
	require.NoError(t, prCmd.PersistentPreRunE(prCmd, nil))
}

func TestPRConfigInitializationFailureUsesJSONContract(t *testing.T) {
	if os.Getenv("KWT_TEST_PR_CONFIG_INIT_FAILURE") == "1" {
		rootCmd.SetArgs([]string{"pr", "list", "--project", "widget"})
		rootCmd.SetOut(os.Stdout)
		rootCmd.SetErr(os.Stderr)
		Execute()
		return
	}

	kwtHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(kwtHome, "config.toml"), []byte("invalid = [\n"), 0o600))
	cmd := exec.Command(os.Args[0], "-test.run=^TestPRConfigInitializationFailureUsesJSONContract$")
	cmd.Env = append(os.Environ(),
		"KWT_TEST_PR_CONFIG_INIT_FAILURE=1",
		"KWT_HOME="+kwtHome,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 9, exitErr.ExitCode())
	var envelope pullrequest.ErrorEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
	assert.Equal(t, pullrequest.CodeWorkspaceCreation, envelope.Error.Code)
	assert.Contains(t, stderr.String(), "workspace_creation_failed")
}

func TestRunPRListWritesStructuredOutput(t *testing.T) {
	service := &fakePRService{prs: []pullrequest.PullRequest{{
		ID: "github:github.com/acme/widget#17", Number: 17, Title: "Improve widgets",
	}}}
	withPRCommandDeps(t, testPRConfig(), service)
	prState = "all"
	cmd, stdout, stderr := prTestCommand()

	err := runPRList(cmd, nil)

	require.NoError(t, err)
	var envelope struct {
		PullRequests []pullrequest.PullRequest `json:"pull_requests"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
	require.Len(t, envelope.PullRequests, 1)
	assert.Equal(t, 17, envelope.PullRequests[0].Number)
	assert.Equal(t, "all", service.gotState)
	assert.Equal(t, "github.com/acme/widget", service.gotProject.Identity)
	assert.Empty(t, stderr.String())
}

func TestRunPRImportWritesCreatedAndAlreadyImportedResults(t *testing.T) {
	for _, status := range []pullrequest.ImportStatus{pullrequest.ImportCreated, pullrequest.ImportExisting} {
		t.Run(string(status), func(t *testing.T) {
			service := &fakePRService{result: pullrequest.ImportResult{
				Status: status, Project: pullrequest.Project{Identity: "github.com/acme/widget"},
				Workspace: pullrequest.Workspace{ID: "ws", Path: "/worktrees/ws", SessionName: "kwt-workspace-ws"},
			}}
			withPRCommandDeps(t, testPRConfig(), service)
			cmd, stdout, _ := prTestCommand()

			err := runPRImport(cmd, []string{"https://github.com/acme/widget/pull/17"})

			require.NoError(t, err)
			var got pullrequest.ImportResult
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
			assert.Equal(t, status, got.Status)
			assert.Equal(t, "https://github.com/acme/widget/pull/17", service.gotSelector)
		})
	}
}

func TestRunPRImportStartsCanonicalWorkspaceSessionOnRequest(t *testing.T) {
	workspace := pullrequest.Workspace{
		ID: "ws", Path: "/worktrees/ws",
		SessionName: "kwt-workspace-ws",
	}
	service := &fakePRService{result: pullrequest.ImportResult{
		Status:    pullrequest.ImportCreated,
		Project:   pullrequest.Project{Identity: "github.com/acme/widget"},
		Workspace: workspace,
	}}
	cfg := testPRConfig()
	withPRCommandDeps(t, cfg, service)
	prStartSession = true
	var started bool
	startPRWorkspaceSession = func(
		_ context.Context,
		got pullrequest.Workspace,
		gotConfig *models.Config,
	) error {
		started = true
		assert.Equal(t, workspace, got)
		assert.Same(t, cfg, gotConfig)
		return nil
	}
	cmd, _, _ := prTestCommand()

	err := runPRImport(
		cmd,
		[]string{"https://github.com/acme/widget/pull/17"},
	)

	require.NoError(t, err)
	assert.True(t, started)
}

func TestPRCommandWritesTypedJSONErrorAndExitStatus(t *testing.T) {
	service := &fakePRService{listErr: pullrequest.NewError(
		pullrequest.CodeAuthentication, "GitHub authentication failed", false, errors.New("secret cause"))}
	withPRCommandDeps(t, testPRConfig(), service)
	cmd, stdout, stderr := prTestCommand()

	err := runPRList(cmd, nil)

	var exitErr *prCommandError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode())
	var envelope pullrequest.ErrorEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &envelope))
	require.NotNil(t, envelope.Error)
	assert.Equal(t, pullrequest.CodeAuthentication, envelope.Error.Code)
	assert.NotContains(t, stdout.String(), "secret cause")
	assert.Contains(t, stderr.String(), "authentication_failed")
}

func TestPRFailureCategoriesHaveDistinctExitStatuses(t *testing.T) {
	codes := []pullrequest.ErrorCode{
		pullrequest.CodeAuthentication,
		pullrequest.CodeRepositoryMismatch,
		pullrequest.CodeNotFound,
		pullrequest.CodeInaccessibleHead,
		pullrequest.CodeNamingConflict,
		pullrequest.CodeNetwork,
		pullrequest.CodeWorkspaceCreation,
		pullrequest.CodeMalformedResponse,
		pullrequest.CodeConflict,
		pullrequest.CodeUnsupportedGitVersion,
	}
	seen := make(map[int]pullrequest.ErrorCode)
	for _, code := range codes {
		exit := prExitCode(code)
		if previous, ok := seen[exit]; ok {
			t.Fatalf("%s and %s share exit status %d", previous, code, exit)
		}
		seen[exit] = code
	}
}

func TestResolvePRProjectSupportsStableIdentityNameAndPath(t *testing.T) {
	otherPath, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	widgetPath, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfg := &models.Config{Projects: []models.Project{
		{Repository: "github.com/acme/other", Name: "other", Path: otherPath},
		{Repository: "github.com/acme/widget", Name: "widget", Path: widgetPath},
	}}
	for _, selector := range []string{"github.com/acme/widget", "widget", widgetPath} {
		t.Run(selector, func(t *testing.T) {
			project, err := resolvePRProject(cfg, selector)
			require.NoError(t, err)
			assert.Equal(t, "github.com/acme/widget", project.Identity)
			assert.Equal(t, widgetPath, project.Path)
		})
	}
}

func TestResolvePRProjectRejectsAmbiguousProjectName(t *testing.T) {
	acmePath, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	octocatPath, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfg := &models.Config{Projects: []models.Project{
		{Repository: "github.com/acme/widget", Name: "widget", Path: acmePath},
		{Repository: "github.com/octocat/widget", Name: "Widget", Path: octocatPath},
	}}

	_, err = resolvePRProject(cfg, "widget")

	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)
	assert.Contains(t, err.Error(), "ambiguous")

	for _, selector := range []string{"github.com/octocat/widget", octocatPath} {
		project, selectErr := resolvePRProject(cfg, selector)
		require.NoError(t, selectErr)
		assert.Equal(t, "github.com/octocat/widget", project.Identity)
	}
}

func TestResolvePRProjectPrefersIdentityAndNameOverCallerRelativePaths(t *testing.T) {
	caller := t.TempDir()
	changeDir(t, caller)
	identityCollision := filepath.Join(caller, "github.com", "acme", "widget")
	nameCollision := filepath.Join(caller, "widget")
	require.NoError(t, os.MkdirAll(identityCollision, 0o755))
	require.NoError(t, os.MkdirAll(nameCollision, 0o755))
	desiredPath := t.TempDir()
	cfg := &models.Config{Projects: []models.Project{
		{Repository: "github.com/attacker/identity-collision", Name: "identity-collision", Path: identityCollision},
		{Repository: "github.com/attacker/name-collision", Name: "name-collision", Path: nameCollision},
		{Repository: "github.com/acme/widget", Name: "widget", Path: desiredPath},
	}}

	for _, selector := range []string{"github.com/acme/widget", "widget"} {
		project, err := resolvePRProject(cfg, selector)

		require.NoError(t, err)
		assert.Equal(t, "github.com/acme/widget", project.Identity)
		assert.Equal(t, desiredPath, project.Path)
	}
}

func TestResolvePRProjectRejectsRelativeAndSymlinkPathSelectors(t *testing.T) {
	caller := t.TempDir()
	changeDir(t, caller)
	projectPath := filepath.Join(caller, "repos", "widget")
	require.NoError(t, os.MkdirAll(projectPath, 0o755))
	cfg := &models.Config{Projects: []models.Project{{
		Repository: "github.com/acme/widget", Name: "widget", Path: projectPath,
	}}}

	_, err := resolvePRProject(cfg, filepath.Join("repos", "widget"))
	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)

	symlinkPath := filepath.Join(caller, "widget-link")
	if symlinkErr := os.Symlink(projectPath, symlinkPath); symlinkErr != nil {
		t.Skipf("symlinks unavailable: %v", symlinkErr)
	}
	_, err = resolvePRProject(cfg, symlinkPath)
	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)
}

func TestValidatePRProjectNormalizesGitHubIdentityCase(t *testing.T) {
	project, err := validatePRProject(pullrequest.Project{
		Identity: "GitHub.com/Acme/Widget", Name: "widget", Path: "/repos/widget",
	})

	require.NoError(t, err)
	assert.Equal(t, "github.com/acme/widget", project.Identity)
}

func TestValidatePRProjectRejectsEmptyPath(t *testing.T) {
	_, err := validatePRProject(pullrequest.Project{
		Identity: "github.com/acme/widget", Name: "widget",
	})

	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)
}

func TestDefaultValidatePRProjectRootRejectsCallerRelativePath(t *testing.T) {
	_, err := defaultValidatePRProjectRoot(pullrequest.Project{
		Identity: "github.com/acme/widget", Name: "widget", Path: ".",
	})

	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)
}

func TestResolvePRProjectRejectsMismatchAndUnsupportedProvider(t *testing.T) {
	cfg := testPRConfig()
	_, err := resolvePRProject(cfg, "missing")
	assertPRCode(t, err, pullrequest.CodeRepositoryMismatch)

	cfg.Projects[0].Repository = "gitlab.com/acme/widget"
	_, err = resolvePRProject(cfg, "widget")
	assertPRCode(t, err, pullrequest.CodeUnsupportedProvider)
}

func assertPRCode(t *testing.T, err error, code pullrequest.ErrorCode) {
	t.Helper()
	var typed *pullrequest.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, code, typed.Code)
}
