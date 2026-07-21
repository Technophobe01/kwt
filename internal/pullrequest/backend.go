package pullrequest

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	gitadapter "go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/template"
	"go.kenn.io/kwt/internal/tmux"
	urlutil "go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/worktree"
)

type GitBackend struct {
	git     *gitadapter.Git
	manager *worktree.Manager
	project Project
}

func NewGitBackend(g *gitadapter.Git, manager *worktree.Manager, project Project) *GitBackend {
	return &GitBackend{git: g, manager: manager, project: project}
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
		result = append(result, Workspace{
			ID:         b.project.Identity + ":" + candidate.Branch + ":" + template.ShortHash(candidate.Path),
			Repository: b.project.Identity, Branch: candidate.Branch, Path: candidate.Path,
			State: "ready", SessionName: tmux.WorkspaceSessionName(info, candidate.Branch, candidate.Path),
		})
	}
	return result, nil
}

func (b *GitBackend) BranchExists(ctx context.Context, branch string) (bool, error) {
	output, err := b.git.RunWithContext(ctx, "for-each-ref", "--format=%(refname)", "refs/heads")
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

func (b *GitBackend) EnsureRemote(ctx context.Context, repository Repository) (string, error) {
	output, err := b.git.RunWithContext(ctx, "remote")
	if err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to list Git remotes", false, err)
	}
	existing := make(map[string]bool)
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		existing[name] = true
		fetchURL, fetchErr := b.git.RunWithContext(ctx, "remote", "get-url", name)
		pushURL, pushErr := b.git.RunWithContext(ctx, "remote", "get-url", "--push", name)
		if fetchErr != nil || pushErr != nil {
			continue
		}
		fetchIdentity, fetchOK := urlutil.CanonicalRepositoryIdentityFromRemote(strings.TrimSpace(fetchURL))
		pushIdentity, pushOK := urlutil.CanonicalRepositoryIdentityFromRemote(strings.TrimSpace(pushURL))
		if fetchOK && pushOK && fetchIdentity == repository.Identity && pushIdentity == repository.Identity {
			return name, nil
		}
	}
	if strings.TrimSpace(repository.CloneURL) == "" {
		return "", NewError(CodeInaccessibleHead, "pull-request head repository has no clone URL", false, nil)
	}
	base := "kwt-pr-" + sanitizeRemoteComponent(repository.Owner)
	name := base
	for suffix := 2; existing[name]; suffix++ {
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	if _, err := b.git.RunWithContext(ctx, "remote", "add", name, repository.CloneURL); err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to add pull-request Git remote", false, err)
	}
	return name, nil
}

func (b *GitBackend) Fetch(ctx context.Context, remote, sourceRef, destinationRef string) (string, error) {
	refspec := "+" + sourceRef + ":" + destinationRef
	if _, err := b.git.RunNonInteractiveWithContext(ctx, "fetch", "--no-tags", remote, refspec); err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "authentication failed"), strings.Contains(message, "permission denied"):
			return "", NewError(CodeAuthentication, "Git authentication failed while fetching the pull-request head", false, err)
		case strings.Contains(message, "could not resolve"), strings.Contains(message, "unable to access"),
			strings.Contains(message, "connection timed out"), strings.Contains(message, "connection refused"):
			return "", NewError(CodeNetwork, "network failure while fetching the pull-request head", true, err)
		default:
			return "", NewError(CodeInaccessibleHead, "pull-request head ref is unavailable", false, err)
		}
	}
	sha, err := b.git.RunWithContext(ctx, "rev-parse", "--verify", destinationRef+"^{commit}")
	if err != nil {
		return "", NewError(CodeInaccessibleHead, "fetched pull-request head is not a commit", false, err)
	}
	return strings.TrimSpace(sha), nil
}

func (b *GitBackend) Create(ctx context.Context, branch, baseRef string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	path, err := b.manager.AddFromBase(branch, baseRef, "")
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "already exists") || strings.Contains(message, "already checked out") ||
			strings.Contains(message, "not an empty directory") || strings.Contains(message, "already registered worktree") {
			return Workspace{}, NewError(CodeNamingConflict,
				"the generated pull-request branch or workspace path is already in use", false, err)
		}
		return Workspace{}, err
	}
	info, ok := urlutil.CanonicalRepositoryInfo(b.project.Identity)
	if !ok {
		return Workspace{}, fmt.Errorf("invalid project repository identity %q", b.project.Identity)
	}
	return Workspace{
		ID:         b.project.Identity + ":" + branch + ":" + template.ShortHash(path),
		Repository: b.project.Identity, Branch: branch, Path: path, State: "ready",
		SessionName: tmux.WorkspaceSessionName(info, branch, path),
	}, nil
}

func (b *GitBackend) ConfigurePush(ctx context.Context, workspace Workspace, remote, sourceBranch string) error {
	commands := [][]string{
		{"config", "extensions.worktreeConfig", "true"},
		{"-C", workspace.Path, "config", "branch." + workspace.Branch + ".remote", remote},
		{"-C", workspace.Path, "config", "branch." + workspace.Branch + ".merge", "refs/heads/" + sourceBranch},
		{"-C", workspace.Path, "config", "--worktree", "push.default", "upstream"},
	}
	for _, args := range commands {
		if _, err := b.git.RunWithContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (b *GitBackend) Rollback(ctx context.Context, workspace Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.manager.RemoveWithBranch(workspace.Path, workspace.Branch, true, true, true)
}

func sanitizeRemoteComponent(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "head"
	}
	return result
}
