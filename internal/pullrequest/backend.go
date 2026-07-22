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
	path, err := b.manager.PreparePath("", branch)
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
		return workspace, mapSharedChangeRequestError(createErr)
	}
	return workspace, nil
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
