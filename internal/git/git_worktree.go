package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/kwt/pkg/models"
)

// ListWorktrees returns a list of all worktrees in the repository.
func (g *Git) ListWorktrees() ([]models.Worktree, error) {
	output, err := g.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	var worktrees []models.Worktree
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i := 0; i < len(lines); i++ {
		if after, ok := strings.CutPrefix(lines[i], "worktree "); ok {
			path := after

			var branch, commitHash string
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
				}
				i = j
			}

			if branch == "" {
				branch = g.getCurrentBranch(path)
			}

			info, err := os.Stat(path)
			var createdAt time.Time
			if err == nil {
				createdAt = info.ModTime()
			}

			worktrees = append(worktrees, models.Worktree{
				Path:       path,
				Branch:     branch,
				CommitHash: commitHash,
				IsMain:     isMain,
				CreatedAt:  createdAt,
			})
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
	args := []string{"worktree", "add", "-b", branch, path}

	if baseBranch != "" {
		args = append(args, baseBranch)
	}

	if _, err := g.run(args...); err != nil {
		return fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
	}

	return nil
}

// RemoveWorktree removes a worktree.
func (g *Git) RemoveWorktree(path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)

	if _, err := g.run(args...); err != nil {
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
