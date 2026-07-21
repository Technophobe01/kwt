package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	oldNew := newPRService
	oldProject := prProject
	oldState := prState
	t.Cleanup(func() {
		loadPRConfig = oldLoad
		newPRService = oldNew
		prProject = oldProject
		prState = oldState
	})
	loadPRConfig = func() (*models.Config, error) { return cfg, nil }
	newPRService = func(context.Context, *models.Config, pullrequest.Project) (prService, error) { return service, nil }
	prProject = "widget"
	prState = "open"
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
	cfg := &models.Config{Projects: []models.Project{
		{Repository: "github.com/acme/other", Name: "other", Path: "/repos/other"},
		{Repository: "github.com/acme/widget", Name: "widget", Path: "/repos/widget"},
	}}
	for _, selector := range []string{"github.com/acme/widget", "widget", "/repos/widget"} {
		t.Run(selector, func(t *testing.T) {
			project, err := resolvePRProject(cfg, selector)
			require.NoError(t, err)
			assert.Equal(t, "github.com/acme/widget", project.Identity)
			assert.Equal(t, "/repos/widget", project.Path)
		})
	}
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
