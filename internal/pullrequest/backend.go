package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	managedworktree "go.kenn.io/kit/git/managed"
	gitadapter "go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/template"
	"go.kenn.io/kwt/internal/tmux"
	urlutil "go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/worktree"
)

var createMergeRequestWorktree = managedworktree.CreateWorktreeFromMergeRequest

type GitBackend struct {
	git            *gitadapter.Git
	manager        *worktree.Manager
	project        Project
	gitEnvironment []string
}

func NewGitBackend(
	g *gitadapter.Git,
	manager *worktree.Manager,
	project Project,
) *GitBackend {
	return &GitBackend{
		git: g, manager: manager, project: project,
		gitEnvironment: SafeGitEnvironment(os.Environ()),
	}
}

// SafeGitEnvironment retains ordinary Git authentication context while
// removing credentials and configuration locators owned by KWT.
func SafeGitEnvironment(environment []string) []string {
	blocked := map[string]bool{
		"kwt_github_token": true,
		"kwt_fleet_token":  true,
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		normalizedName := strings.ToLower(name)
		if !strings.HasPrefix(normalizedName, "kwt_") && !blocked[normalizedName] {
			result = append(result, entry)
		}
	}
	return result
}

func (b *GitBackend) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	worktrees, err := b.manager.List()
	if err != nil {
		return nil, err
	}
	info, ok := urlutil.CanonicalRepositoryInfo(b.project.Identity)
	if !ok {
		return nil, fmt.Errorf("invalid project repository identity %q", b.project.Identity)
	}
	result := make([]Workspace, 0, len(worktrees))
	for _, candidate := range worktrees {
		pathInfo, statErr := os.Stat(candidate.Path)
		if candidate.Prunable || statErr != nil || !pathInfo.IsDir() {
			continue
		}
		result = append(result, Workspace{
			ID:         b.project.Identity + ":" + candidate.Branch + ":" + template.ShortHash(candidate.Path),
			Repository: b.project.Identity,
			Branch:     candidate.Branch,
			Path:       candidate.Path,
			State:      "ready",
			SessionName: tmux.WorkspaceSessionName(
				info, candidate.Branch, candidate.Path,
			),
		})
	}
	return result, nil
}

func (b *GitBackend) ImportPullRequest(
	ctx context.Context, pr PullRequest, branch string,
) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	projectRoot, err := b.git.GetMainRepositoryPath()
	if err != nil {
		return Workspace{}, err
	}
	path, err := b.manager.PreparePathForRepository(
		"", branch, b.project.Identity,
	)
	if err != nil {
		return Workspace{}, err
	}
	runner := gitcmd.New()
	runner.Env = append([]string(nil), b.gitEnvironment...)
	runner.DisableSafeDirectoryForward = true
	created, createErr := createMergeRequestWorktree(
		ctx, managedworktree.MergeRequestWorktreeOptions{
			ProjectRoot: projectRoot, Path: path, Branch: branch,
			Runner: runner, HookEnvironmentPrefix: "KWT",
			Number: pr.Number, HeadBranch: pr.Source.Name,
			HeadRepoCloneURL: pr.Source.Repository.CloneURL,
			ExpectedHeadSHA:  pr.HeadSHA, Platform: pr.Provider,
			ProjectRepoIdentity: b.project.Identity,
		},
	)
	if created.Path == "" {
		return Workspace{}, mapSharedChangeRequestError(createErr)
	}
	info, ok := urlutil.CanonicalRepositoryInfo(b.project.Identity)
	if !ok {
		return Workspace{}, fmt.Errorf(
			"invalid project repository identity %q", b.project.Identity,
		)
	}
	workspace := Workspace{
		ID:          b.project.Identity + ":" + branch + ":" + template.ShortHash(created.Path),
		Repository:  b.project.Identity,
		Branch:      branch,
		Path:        created.Path,
		State:       "ready",
		SessionName: tmux.WorkspaceSessionName(info, branch, created.Path),
	}
	workspace.partialCleanup = &workspacePartialCleanup{run: func(cleanupCtx context.Context) error {
		remaining, cleanupErr := created.Rollback(cleanupCtx)
		if cleanupErr != nil {
			return cleanupErr
		}
		if remaining.Path != "" || remaining.Branch != "" {
			return fmt.Errorf(
				"ownership-safe cleanup preserved path %q and branch %q",
				remaining.Path, remaining.Branch,
			)
		}
		return nil
	}}
	if createErr != nil {
		workspace.preserveOnImportError = errors.Is(
			createErr, managedworktree.ErrWorktreeCleanupIncomplete,
		)
		return workspace, mapSharedChangeRequestError(createErr)
	}
	if err := ensureForkPushSafety(
		ctx, runner, created.Path, branch,
		pr.Source.Repository.Identity, pr.Source.Name,
		!EqualRepositoryIdentity(
			pr.Source.Repository.Identity, pr.Repository.Identity,
		),
	); err != nil {
		return workspace, NewError(
			CodeWorkspaceCreation,
			"failed to make fork push routing safe",
			false, err,
		)
	}
	return workspace, nil
}

func ensureForkPushSafety(
	ctx context.Context,
	runner gitcmd.Runner,
	path, branch string,
	sourceRepository, sourceBranch string,
	fork bool,
) error {
	if !fork {
		return nil
	}

	pushRemote, err := effectiveGitConfig(
		ctx, runner, path, "branch."+branch+".pushRemote",
	)
	if err != nil {
		return err
	}
	trackingRemote, err := effectiveGitConfig(
		ctx, runner, path, "branch."+branch+".remote",
	)
	if err != nil {
		return err
	}
	mergeRef, err := effectiveGitConfig(
		ctx, runner, path, "branch."+branch+".merge",
	)
	if err != nil {
		return err
	}
	pushDefault, err := effectiveGitConfig(
		ctx, runner, path, "push.default",
	)
	if err != nil {
		return err
	}
	if pushRemote == "" ||
		trackingRemote != pushRemote ||
		mergeRef != "refs/heads/"+sourceBranch ||
		pushDefault != "upstream" {
		return errors.New("kit did not establish exact fork push tracking")
	}
	if strings.HasPrefix(pushRemote, "-") ||
		strings.ContainsAny(pushRemote, " \t\r\n") {
		return errors.New("kit configured an invalid fork push remote")
	}

	pushURLs, err := effectivePushURLs(ctx, runner, path, pushRemote)
	if err != nil {
		return err
	}
	if len(pushURLs) != 1 {
		return errors.New("fork push remote does not have exactly one destination")
	}
	pushIdentity, ok := urlutil.CanonicalRepositoryIdentityFromRemote(pushURLs[0])
	if !ok || !EqualRepositoryIdentity(pushIdentity, sourceRepository) {
		return errors.New("fork push remote does not target the pull-request repository")
	}

	for _, key := range []string{
		"remote." + pushRemote + ".push",
		"remote." + pushRemote + ".receivepack",
	} {
		values, configErr := gitConfigValues(ctx, runner, path, key, true)
		if configErr != nil {
			return configErr
		}
		if len(values) != 0 {
			return fmt.Errorf("fork push remote has unsupported %s configuration", key)
		}
	}
	mirror, err := effectiveGitConfig(
		ctx, runner, path, "remote."+pushRemote+".mirror",
	)
	if err != nil {
		return err
	}
	if mirror != "" && !strings.EqualFold(mirror, "false") {
		return errors.New("fork push remote is configured as a mirror")
	}
	return nil
}

func effectiveGitConfig(
	ctx context.Context,
	runner gitcmd.Runner,
	path, key string,
) (string, error) {
	values, err := gitConfigValues(ctx, runner, path, key, false)
	if err != nil || len(values) == 0 {
		return "", err
	}
	value := values[0]
	if value == "" || value != strings.TrimSpace(value) ||
		strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("git configuration %s has an invalid value", key)
	}
	return value, nil
}

func gitConfigValues(
	ctx context.Context,
	runner gitcmd.Runner,
	path, key string,
	all bool,
) ([]string, error) {
	operation := "--get"
	if all {
		operation = "--get-all"
	}
	stdout, _, err := runner.Run(
		ctx, path, nil, "config", "--null", operation, key,
	)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if gitcmd.IsExitCode(err, 1) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect git configuration %s: %w", key, err)
	}
	encoded := strings.TrimSuffix(string(stdout), "\x00")
	if encoded == "" {
		return []string{""}, nil
	}
	return strings.Split(encoded, "\x00"), nil
}

func effectivePushURLs(
	ctx context.Context,
	runner gitcmd.Runner,
	path, remote string,
) ([]string, error) {
	stdout, _, err := runner.Run(
		ctx, path, nil, "remote", "get-url", "--push", "--all", remote,
	)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("inspect fork push destination: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")
	for _, line := range lines {
		if line == "" || line != strings.TrimSpace(line) ||
			strings.ContainsRune(line, '\r') {
			return nil, errors.New("fork push remote has an invalid destination")
		}
	}
	return lines, nil
}

func mapSharedChangeRequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, managedworktree.ErrBranchInUse) ||
		errors.Is(err, managedworktree.ErrWorktreeDestinationExists) ||
		errors.Is(err, managedworktree.ErrInvalidBranchName) {
		return NewError(
			CodeNamingConflict,
			"the generated pull-request branch or workspace path is already in use",
			false, err,
		)
	}
	var shared *managedworktree.ChangeRequestError
	if !errors.As(err, &shared) {
		return err
	}
	code := CodeWorkspaceCreation
	retryable := false
	switch shared.Kind {
	case managedworktree.ChangeRequestAuthentication:
		code = CodeAuthentication
	case managedworktree.ChangeRequestNetwork:
		code = CodeNetwork
		retryable = true
	case managedworktree.ChangeRequestInaccessibleHead:
		code = CodeInaccessibleHead
	case managedworktree.ChangeRequestHeadChanged:
		code = CodeConflict
		retryable = true
	case managedworktree.ChangeRequestUnsupportedGit:
		code = CodeUnsupportedGitVersion
	}
	return NewError(code, shared.Message, retryable, err)
}

func (b *GitBackend) Rollback(ctx context.Context, workspace Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if workspace.partialCleanup != nil {
		return workspace.partialCleanup.run(ctx)
	}
	return fmt.Errorf(
		"ownership-safe rollback metadata is unavailable for path %q and branch %q",
		workspace.Path, workspace.Branch,
	)
}
