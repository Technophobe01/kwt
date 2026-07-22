package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	managedworktree "go.kenn.io/kit/git/managed"
	gitremote "go.kenn.io/kit/git/remote"
	gitadapter "go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/template"
	"go.kenn.io/kwt/internal/tmux"
	urlutil "go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/worktree"
)

type GitBackend struct {
	git                       *gitadapter.Git
	manager                   *worktree.Manager
	project                   Project
	fleetTokenEnv             string
	fleetTokenFileEnvironment []string
	setupEnvironment          []string
	shared                    *managedworktree.ChangeRequestGit
	sharedErr                 error
}

type GitBackendOption func(*GitBackend)

func WithFleetTokenEnvironment(name string) GitBackendOption {
	return func(backend *GitBackend) {
		backend.fleetTokenEnv = name
	}
}

func WithFleetTokenFileEnvironment(names []string) GitBackendOption {
	return func(backend *GitBackend) {
		backend.fleetTokenFileEnvironment = append([]string(nil), names...)
	}
}

func NewGitBackend(
	g *gitadapter.Git,
	manager *worktree.Manager,
	project Project,
	options ...GitBackendOption,
) *GitBackend {
	backend := &GitBackend{git: g, manager: manager, project: project}
	for _, option := range options {
		option(backend)
	}
	blockedEnvironment := append(
		[]string{backend.fleetTokenEnv}, backend.fleetTokenFileEnvironment...,
	)
	backend.setupEnvironment = SafeSetupEnvironment(
		os.Environ(), blockedEnvironment...,
	)

	runner := gitcmd.New()
	runner.Env = append([]string(nil), backend.setupEnvironment...)
	runner.DisableSafeDirectoryForward = true
	info, _ := urlutil.CanonicalRepositoryInfo(project.Identity)
	identity := gitremote.Identity{}
	if info != nil {
		identity = gitremote.Identity{
			Host: info.Host, Owner: info.Owner, Name: info.Repository,
		}
	}
	projectRoot, rootErr := g.GetMainRepositoryPath()
	if rootErr != nil {
		backend.sharedErr = rootErr
		return backend
	}
	backend.shared, backend.sharedErr = managedworktree.NewChangeRequestGit(
		managedworktree.ChangeRequestGitOptions{
			ProjectRoot: projectRoot, ProjectIdentity: identity,
			RemoteNamePrefix: "kwt-pr", HookIsolationNamespace: "kwt",
			Runner: runner,
		},
	)
	return backend
}

// SafeSetupEnvironment retains the user's ordinary setup environment while
// removing credentials owned or sourced by kwt.
func SafeSetupEnvironment(environment []string, fleetTokenEnvironment ...string) []string {
	blocked := map[string]bool{
		"kwt_github_token": true,
		"kwt_fleet_token":  true,
	}
	for _, environmentName := range fleetTokenEnvironment {
		if name := strings.TrimSpace(environmentName); name != "" {
			blocked[strings.ToLower(name)] = true
		}
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

func (b *GitBackend) ValidateImport(ctx context.Context) error {
	if b.sharedErr != nil {
		return NewError(
			CodeWorkspaceCreation,
			"failed to initialize shared pull-request Git boundary",
			false,
			b.sharedErr,
		)
	}
	return mapSharedChangeRequestError(b.shared.Validate(ctx))
}

func (b *GitBackend) validateWorktreeConfiguration(
	ctx context.Context, worktreePath string,
) error {
	return mapSharedChangeRequestError(
		b.shared.ValidateWorktree(ctx, worktreePath),
	)
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

func (b *GitBackend) BranchExists(ctx context.Context, branch string) (bool, error) {
	output, err := b.git.RunWithContext(
		ctx, "for-each-ref", "--format=%(refname)", "refs/heads",
	)
	if err != nil {
		return false, err
	}
	want := "refs/heads/" + branch
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == want {
			return true, nil
		}
	}
	return false, nil
}

func (b *GitBackend) EnsureRemote(
	ctx context.Context, repository Repository,
) (string, error) {
	remote, err := b.shared.EnsureRemote(ctx, sharedRemoteRepository(repository))
	return remote, mapSharedChangeRequestError(err)
}

func (b *GitBackend) Fetch(
	ctx context.Context, remote, sourceRef, destinationRef, expectedOID string,
) (string, error) {
	sha, err := b.shared.FetchExpected(ctx, remote, sourceRef, destinationRef, expectedOID)
	return sha, mapSharedChangeRequestError(err)
}

func (b *GitBackend) Create(
	ctx context.Context, branch, baseRef string,
) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	var ownershipCleanup func(context.Context) (string, string, error)
	path, createErr := b.manager.AddFromBaseWithOptions(
		branch,
		baseRef,
		"",
		worktree.AddOptions{
			Context: ctx, SkipSetup: true,
			SetupEnvironment: b.setupEnvironment,
			BeforeCheckout:   b.validateWorktreeConfiguration,
			CaptureCleanup: func(cleanup func(context.Context) (string, string, error)) {
				ownershipCleanup = cleanup
			},
		},
	)
	if createErr != nil && path == "" {
		message := strings.ToLower(createErr.Error())
		if strings.Contains(message, "already exists") ||
			strings.Contains(message, "already checked out") ||
			strings.Contains(message, "not an empty directory") ||
			strings.Contains(message, "already registered worktree") {
			return Workspace{}, NewError(
				CodeNamingConflict,
				"the generated pull-request branch or workspace path is already in use",
				false,
				createErr,
			)
		}
		return Workspace{}, createErr
	}
	info, ok := urlutil.CanonicalRepositoryInfo(b.project.Identity)
	if !ok {
		return Workspace{}, fmt.Errorf(
			"invalid project repository identity %q", b.project.Identity,
		)
	}
	workspace := Workspace{
		ID:          b.project.Identity + ":" + branch + ":" + template.ShortHash(path),
		Repository:  b.project.Identity,
		Branch:      branch,
		Path:        path,
		State:       "ready",
		SessionName: tmux.WorkspaceSessionName(info, branch, path),
	}
	if ownershipCleanup != nil {
		workspace.partialCleanup = &workspacePartialCleanup{
			run: func(cleanupCtx context.Context) error {
				remainingPath, remainingBranch, cleanupErr := ownershipCleanup(cleanupCtx)
				if cleanupErr != nil {
					return cleanupErr
				}
				if remainingPath != "" || remainingBranch != "" {
					return fmt.Errorf(
						"ownership-safe cleanup preserved path %q and branch %q",
						remainingPath,
						remainingBranch,
					)
				}
				return nil
			},
		}
	}
	if createErr != nil {
		return workspace, createErr
	}
	if validateErr := b.validateWorktreeConfiguration(ctx, workspace.Path); validateErr != nil {
		return workspace, validateErr
	}
	return workspace, nil
}

func (b *GitBackend) ConfigurePush(
	ctx context.Context,
	workspace Workspace,
	remote, sourceRepository, sourceBranch string,
) error {
	repositoryInfo, ok := urlutil.CanonicalRepositoryInfo(sourceRepository)
	if !ok {
		return NewError(
			CodeWorkspaceCreation,
			"invalid pull-request source repository identity",
			false,
			nil,
		)
	}
	repository := managedworktree.RemoteRepository{
		Identity: gitremote.Identity{
			Host: repositoryInfo.Host, Owner: repositoryInfo.Owner,
			Name: repositoryInfo.Repository,
		},
	}
	return mapSharedChangeRequestError(
		b.shared.ConfigurePush(
			ctx,
			managedworktree.CreateWorktreeResult{
				Path: workspace.Path, Branch: workspace.Branch,
			},
			remote,
			repository,
			sourceBranch,
		),
	)
}

func sharedRemoteRepository(repository Repository) managedworktree.RemoteRepository {
	return managedworktree.RemoteRepository{
		Identity: gitremote.Identity{
			Host: repository.Host, Owner: repository.Owner, Name: repository.Name,
		},
		CloneURL: repository.CloneURL,
	}
}

func mapSharedChangeRequestError(err error) error {
	if err == nil {
		return nil
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
