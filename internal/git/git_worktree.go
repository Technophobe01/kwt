package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/kwt/pkg/models"
)

// PartialWorktreeCreationError reports repository state that remained after a
// failed worktree add and its internal cleanup attempt.
type PartialWorktreeCreationError struct {
	Path   string
	Branch string
	Err    error
}

func (e *PartialWorktreeCreationError) Error() string { return e.Err.Error() }
func (e *PartialWorktreeCreationError) Unwrap() error { return e.Err }
func (e *PartialWorktreeCreationError) PartialWorktree() (string, string) {
	return e.Path, e.Branch
}

// ListWorktrees returns a list of all worktrees in the repository.
func (g *Git) ListWorktrees() ([]models.Worktree, error) {
	output, err := g.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	worktrees := parseWorktreePorcelain(output)
	for i := range worktrees {
		if worktrees[i].Branch == "" {
			worktrees[i].Branch = g.getCurrentBranch(worktrees[i].Path)
		}
		if info, statErr := os.Stat(worktrees[i].Path); statErr == nil {
			worktrees[i].CreatedAt = info.ModTime()
		}
	}

	if len(worktrees) > 0 {
		mainDir, err := g.getMainRepoRoot()
		if err == nil {
			for i := range worktrees {
				resolvedPath := worktrees[i].Path
				if resolved, err := filepath.EvalSymlinks(resolvedPath); err == nil {
					resolvedPath = resolved
				}
				if resolvedPath == mainDir {
					worktrees[i].IsMain = true
					break
				}
			}
		}
	}

	return worktrees, nil
}

func parseWorktreePorcelain(output string) []models.Worktree {
	var worktrees []models.Worktree
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i := 0; i < len(lines); i++ {
		if after, ok := strings.CutPrefix(lines[i], "worktree "); ok {
			path := after

			var branch, commitHash string
			var prunable bool
			isMain := false

			for j := i + 1; j < len(lines) && !strings.HasPrefix(lines[j], "worktree "); j++ {
				if after, ok := strings.CutPrefix(lines[j], "branch "); ok {
					branch = after
					// Remove refs/heads/ prefix if present
					branch = strings.TrimPrefix(branch, "refs/heads/")
				} else if after, ok := strings.CutPrefix(lines[j], "HEAD "); ok {
					commitHash = after
				} else if strings.HasPrefix(lines[j], "bare") {
					continue
				} else if strings.HasPrefix(lines[j], "prunable") {
					prunable = true
				}
				i = j
			}

			worktrees = append(worktrees, models.Worktree{
				Path:       path,
				Branch:     branch,
				CommitHash: commitHash,
				IsMain:     isMain,
				Prunable:   prunable,
			})
		}
	}
	return worktrees
}

// AddWorktree creates a new worktree.
func (g *Git) AddWorktree(path, branch string, createBranch bool) error {
	args := []string{"worktree", "add"}

	if createBranch {
		base, err := g.defaultWorktreeBase()
		if err != nil {
			return err
		}
		args = append(args, "-b", branch, path, base)
	} else {
		args = append(args, path, branch)
	}

	if _, err := g.run(args...); err != nil {
		return fmt.Errorf("failed to add worktree: %w", err)
	}

	return nil
}

func (g *Git) defaultWorktreeBase() (string, error) {
	remoteBase, remoteErr := g.remoteDefaultWorktreeBase()
	if remoteErr == nil {
		return remoteBase, nil
	}

	for _, branch := range []string{"main", "master"} {
		ref := "refs/heads/" + branch
		if g.refExists(ref) {
			return ref, nil
		}
	}

	root, rootErr := g.getMainRepoRoot()
	if rootErr == nil {
		output, branchErr := g.run("-C", root, "symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr == nil {
			ref := "refs/heads/" + strings.TrimSpace(output)
			if g.refExists(ref) {
				return ref, nil
			}
		}
	}

	return "", fmt.Errorf(
		"could not resolve default worktree base: remote default unavailable (%v); no local main, master, or primary worktree branch",
		remoteErr,
	)
}

func (g *Git) remoteDefaultWorktreeBase() (string, error) {
	const ref = "refs/kwt/origin/default"
	if _, err := g.run("fetch", "origin", "+HEAD:"+ref); err != nil {
		return "", fmt.Errorf("fetch origin default branch: %w", err)
	}

	if !g.refExists(ref) {
		return "", fmt.Errorf("fetched origin default ref does not exist")
	}
	return ref, nil
}

func (g *Git) refExists(ref string) bool {
	_, err := g.run("show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// AddWorktreeFromBase creates a new worktree with a branch from a specific base branch.
func (g *Git) AddWorktreeFromBase(path, branch, baseBranch string) error {
	return g.addWorktreeFromBase(context.Background(), path, branch, baseBranch, nil, false)
}

// AddWorktreeFromBaseWithEnvironment creates a worktree with an explicit
// checkout environment and a trusted empty hooks directory.
func (g *Git) AddWorktreeFromBaseWithEnvironment(path, branch, baseBranch string, environment []string) error {
	return g.AddWorktreeFromBaseWithEnvironmentAndContext(context.Background(), path, branch, baseBranch, environment)
}

// AddWorktreeFromBaseWithEnvironmentAndContext creates a worktree with an
// explicit checkout environment while allowing request cancellation to stop
// filters and checkout work.
func (g *Git) AddWorktreeFromBaseWithEnvironmentAndContext(ctx context.Context, path, branch, baseBranch string, environment []string) error {
	return g.addWorktreeFromBase(ctx, path, branch, baseBranch, environment, false)
}

// AddWorktreeFromBaseNoCheckoutWithEnvironmentAndContext prepares a linked
// worktree without materializing contributor-controlled files.
func (g *Git) AddWorktreeFromBaseNoCheckoutWithEnvironmentAndContext(ctx context.Context, path, branch, baseBranch string, environment []string) error {
	return g.addWorktreeFromBase(ctx, path, branch, baseBranch, environment, true)
}

func (g *Git) addWorktreeFromBase(ctx context.Context, path, branch, baseBranch string, environment []string, noCheckout bool) error {
	args := []string{"worktree", "add"}
	if noCheckout {
		args = append(args, "--no-checkout")
	}
	args = append(args, "-b", branch, path)

	if baseBranch != "" {
		args = append(args, baseBranch)
	}

	if environment == nil {
		if _, err := g.RunWithContext(ctx, args...); err != nil {
			return fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
		}
		return nil
	}
	branchExisted := g.refExists("refs/heads/" + branch)
	_, pathStatErr := os.Lstat(path)
	pathExisted := !os.IsNotExist(pathStatErr)
	if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, args...); err != nil {
		addErr := fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
		cleanupErr := g.cleanupFailedWorktreeAdd(context.WithoutCancel(ctx), path, branch, pathExisted, branchExisted, environment)
		if cleanupErr != nil {
			return &PartialWorktreeCreationError{
				Path: path, Branch: branch, Err: errors.Join(addErr, cleanupErr),
			}
		}
		return addErr
	}

	return nil
}

// CheckoutWorktreeWithEnvironmentAndContext materializes a prepared
// no-checkout worktree with hooks disabled and an explicit environment.
func (g *Git) CheckoutWorktreeWithEnvironmentAndContext(ctx context.Context, path string, environment []string) error {
	if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "-C", path, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("failed to check out prepared worktree: %w", err)
	}
	return nil
}

func (g *Git) cleanupFailedWorktreeAdd(ctx context.Context, path, branch string, pathExisted, branchExisted bool, environment []string) error {
	if !pathExisted {
		if registered, _ := g.worktreeRegisteredWithEnvironment(ctx, path, environment); registered {
			_ = g.RemoveWorktreeWithEnvironment(path, true, environment)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clean failed worktree directory: %w", err)
		}
		_, _ = g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "worktree", "prune", "--expire", "now")
	}
	if !branchExisted && g.refExists("refs/heads/"+branch) {
		_ = g.DeleteBranchWithEnvironment(branch, true, environment)
		if g.refExists("refs/heads/" + branch) {
			_, _ = g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "update-ref", "-d", "refs/heads/"+branch)
		}
	}

	var cleanupErrs []error
	if !pathExisted {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed worktree path still exists"))
		}
		if registered, err := g.worktreeRegisteredWithEnvironment(ctx, path, environment); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("verify failed worktree cleanup: %w", err))
		} else if registered {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed worktree remains registered"))
		}
	}
	if !branchExisted && g.refExists("refs/heads/"+branch) {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("failed worktree branch remains registered"))
	}
	return errors.Join(cleanupErrs...)
}

func (g *Git) worktreeRegisteredWithEnvironment(ctx context.Context, path string, environment []string) (bool, error) {
	output, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	wanted := filepath.Clean(path)
	for _, line := range strings.Split(output, "\n") {
		listed, ok := strings.CutPrefix(line, "worktree ")
		if ok && filepath.Clean(listed) == wanted {
			return true, nil
		}
	}
	return false, nil
}

// RemoveWorktree removes a worktree.
func (g *Git) RemoveWorktree(path string, force bool) error {
	return g.removeWorktree(path, force, nil)
}

// RemoveWorktreeWithEnvironment removes a worktree with an explicit
// environment and repository hooks disabled.
func (g *Git) RemoveWorktreeWithEnvironment(path string, force bool, environment []string) error {
	return g.removeWorktree(path, force, environment)
}

func (g *Git) removeWorktree(path string, force bool, environment []string) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)

	var err error
	if environment == nil {
		_, err = g.run(args...)
	} else {
		_, err = g.RunWithEnvironmentAndDisabledHooks(context.Background(), environment, args...)
	}
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// PruneWorktrees removes worktree information for deleted directories.
func (g *Git) PruneWorktrees() error {
	if _, err := g.run("worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}
