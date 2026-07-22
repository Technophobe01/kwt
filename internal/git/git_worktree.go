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
	Path        string
	Branch      string
	Err         error
	reservation *worktreeAddReservation
}

func (e *PartialWorktreeCreationError) Error() string {
	remaining := make([]string, 0, 2)
	if e.Path != "" {
		remaining = append(remaining, fmt.Sprintf("path %q", e.Path))
	}
	if e.Branch != "" {
		remaining = append(remaining, fmt.Sprintf("branch %q", e.Branch))
	}
	return fmt.Sprintf("%v; operation-owned %s still requires manual cleanup", e.Err, strings.Join(remaining, " and "))
}
func (e *PartialWorktreeCreationError) Unwrap() error { return e.Err }

// PartialWorktree exposes only remnants that the failed add operation proved
// it still owned when its initial cleanup completed.
func (e *PartialWorktreeCreationError) PartialWorktree() (string, string) {
	return e.Path, e.Branch
}

// RetryCleanup repeats the ownership-checked cleanup used by the failed add.
// It never falls back to deleting a path or ref solely by name.
func (e *PartialWorktreeCreationError) RetryCleanup(ctx context.Context, g *Git, environment []string) error {
	if e.reservation == nil {
		return fmt.Errorf("partial worktree cleanup reservation is unavailable: %w", e)
	}
	remainingPath, remainingBranch, err := g.cleanupFailedWorktreeAdd(ctx, e.reservation, environment)
	e.Path = remainingPath
	e.Branch = remainingBranch
	return err
}

type worktreeAddReservation struct {
	path        string
	pathInfo    os.FileInfo
	branch      string
	branchRef   string
	branchOID   string
	branchOwned bool
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
	if environment == nil {
		args := []string{"worktree", "add"}
		if noCheckout {
			args = append(args, "--no-checkout")
		}
		args = append(args, "-b", branch, path)
		if baseBranch != "" {
			args = append(args, baseBranch)
		}
		if _, err := g.RunWithContext(ctx, args...); err != nil {
			return fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
		}
		return nil
	}

	reservation, err := g.reserveWorktreeAdd(ctx, path, branch, baseBranch, environment)
	if err != nil {
		return err
	}
	args := []string{"worktree", "add"}
	if noCheckout {
		args = append(args, "--no-checkout")
	}
	args = append(args, path, branch)
	if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, args...); err != nil {
		addErr := fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
		remainingPath, remainingBranch, cleanupErr := g.cleanupFailedWorktreeAdd(
			context.WithoutCancel(ctx), reservation, environment,
		)
		if cleanupErr != nil {
			joinedErr := errors.Join(addErr, cleanupErr)
			if remainingPath != "" || remainingBranch != "" {
				return &PartialWorktreeCreationError{
					Path: remainingPath, Branch: remainingBranch, Err: joinedErr, reservation: reservation,
				}
			}
			return joinedErr
		}
		return addErr
	}

	return nil
}

func (g *Git) reserveWorktreeAdd(ctx context.Context, path, branch, baseBranch string, environment []string) (*worktreeAddReservation, error) {
	baseRef := baseBranch
	if baseRef == "" {
		baseRef = "HEAD"
	}
	output, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment,
		"rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve worktree base branch %s: %w", baseBranch, err)
	}
	branchOID := strings.TrimSpace(output)

	if err := os.Mkdir(path, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("worktree path already exists: %s", path)
		}
		return nil, fmt.Errorf("failed to reserve worktree path %s: %w", path, err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("failed to inspect reserved worktree path %s: %w", path, err)
	}

	reservation := &worktreeAddReservation{
		path: path, pathInfo: pathInfo, branch: branch,
		branchRef: "refs/heads/" + branch, branchOID: branchOID,
	}
	if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment,
		"update-ref", reservation.branchRef, branchOID, ""); err != nil {
		removeErr := os.Remove(path)
		reserveErr := fmt.Errorf("failed to reserve worktree branch %s: %w", branch, err)
		if removeErr != nil {
			return nil, &PartialWorktreeCreationError{
				Path: path, Err: errors.Join(reserveErr, fmt.Errorf("remove reserved worktree path: %w", removeErr)),
				reservation: reservation,
			}
		}
		return nil, reserveErr
	}
	reservation.branchOwned = true
	return reservation, nil
}

// CheckoutWorktreeWithEnvironmentAndContext materializes a prepared
// no-checkout worktree with hooks disabled and an explicit environment.
func (g *Git) CheckoutWorktreeWithEnvironmentAndContext(ctx context.Context, path string, environment []string) error {
	if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "-C", path, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("failed to check out prepared worktree: %w", err)
	}
	return nil
}

func (g *Git) cleanupFailedWorktreeAdd(ctx context.Context, reservation *worktreeAddReservation, environment []string) (string, string, error) {
	var cleanupErrs []error
	pathOwned := reservation.pathInfo != nil && sameFileAtPath(reservation.path, reservation.pathInfo)
	registration, registrationErr := g.worktreeRegistrationWithEnvironment(ctx, reservation.path, environment)
	ownedRegistration := reservation.branchOwned && registration != nil && registration.Branch == reservation.branch &&
		strings.EqualFold(registration.CommitHash, reservation.branchOID)
	if registrationErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("inspect failed worktree registration: %w", registrationErr))
	} else if pathOwned && ownedRegistration {
		if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment,
			"worktree", "remove", "--force", "--force", reservation.path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove reserved worktree: %w", err))
		}
	}

	remainingPath := ""
	pathOwned = reservation.pathInfo != nil && sameFileAtPath(reservation.path, reservation.pathInfo)
	if pathOwned && registrationErr == nil && registration == nil {
		var removeErr error
		remainingPath, removeErr = removeReservedWorktreePath(reservation.path, reservation.pathInfo)
		if removeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove reserved worktree directory: %w", removeErr))
		}
	}
	pathOwned = reservation.pathInfo != nil && sameFileAtPath(reservation.path, reservation.pathInfo)

	branchRegistrations, branchUseErr := g.worktreeBranchRegistrationsWithEnvironment(ctx, reservation.branch, environment)
	if branchUseErr != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("inspect failed worktree branch registration: %w", branchUseErr))
	}
	if reservation.branchOwned && branchUseErr == nil && !pathOwned && registrationsBelongToReservation(branchRegistrations, reservation) {
		_, _ = g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "worktree", "prune", "--expire", "now")
		branchRegistrations, branchUseErr = g.worktreeBranchRegistrationsWithEnvironment(ctx, reservation.branch, environment)
		if branchUseErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("inspect pruned worktree branch registration: %w", branchUseErr))
		}
	}
	branchInUse := len(branchRegistrations) > 0
	if reservation.branchOwned && reservation.branchOID != "" && !branchInUse && branchUseErr == nil {
		if _, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment,
			"update-ref", "-d", reservation.branchRef, reservation.branchOID); err != nil {
			if oid, exists, inspectErr := g.refOIDWithEnvironment(ctx, reservation.branchRef, environment); inspectErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("verify reserved branch cleanup: %w", inspectErr))
			} else if exists && strings.EqualFold(oid, reservation.branchOID) {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("delete reserved worktree branch: %w", err))
			}
		}
	}

	if pathOwned && remainingPath == "" {
		remainingPath = reservation.path
	}
	remainingBranch := ""
	if oid, exists, err := g.refOIDWithEnvironment(ctx, reservation.branchRef, environment); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("inspect reserved branch after cleanup: %w", err))
	} else if reservation.branchOwned && exists && strings.EqualFold(oid, reservation.branchOID) &&
		(!branchInUse || remainingPath != "" && ownedRegistration) {
		remainingBranch = reservation.branch
	}
	if remainingPath != "" {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("reserved worktree path remains"))
	}
	if remainingBranch != "" {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("reserved worktree branch remains"))
	}
	return remainingPath, remainingBranch, errors.Join(cleanupErrs...)
}

func sameFileAtPath(path string, expected os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(expected, current)
}

func removeReservedWorktreePath(path string, expected os.FileInfo) (string, error) {
	if !sameFileAtPath(path, expected) {
		return "", nil
	}
	quarantine, err := os.MkdirTemp(filepath.Dir(path), ".kwt-cleanup-")
	if err != nil {
		return path, err
	}
	if err := os.Remove(quarantine); err != nil {
		return path, err
	}
	if err := os.Rename(path, quarantine); err != nil {
		return path, err
	}
	moved, err := os.Lstat(quarantine)
	if err != nil {
		return quarantine, err
	}
	if !os.SameFile(expected, moved) {
		if _, pathErr := os.Lstat(path); os.IsNotExist(pathErr) {
			if restoreErr := os.Rename(quarantine, path); restoreErr != nil {
				return "", fmt.Errorf("reserved path ownership changed and restore failed: %w", restoreErr)
			}
		}
		return "", nil
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return quarantine, err
	}
	return "", nil
}

func (g *Git) refOIDWithEnvironment(ctx context.Context, ref string, environment []string) (string, bool, error) {
	output, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment,
		"for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return "", false, err
	}
	oid := strings.TrimSpace(output)
	return oid, oid != "", nil
}

func (g *Git) worktreeBranchRegistrationsWithEnvironment(ctx context.Context, branch string, environment []string) ([]models.Worktree, error) {
	output, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var registrations []models.Worktree
	for _, worktree := range parseWorktreePorcelain(output) {
		if worktree.Branch == branch {
			registrations = append(registrations, worktree)
		}
	}
	return registrations, nil
}

func registrationsBelongToReservation(registrations []models.Worktree, reservation *worktreeAddReservation) bool {
	if len(registrations) == 0 {
		return false
	}
	for _, registration := range registrations {
		if canonicalWorktreeAdminPath(registration.Path) != canonicalWorktreeAdminPath(reservation.path) ||
			registration.Branch != reservation.branch ||
			!strings.EqualFold(registration.CommitHash, reservation.branchOID) {
			return false
		}
	}
	return true
}

func (g *Git) worktreeRegistrationWithEnvironment(ctx context.Context, path string, environment []string) (*models.Worktree, error) {
	output, err := g.RunWithEnvironmentAndDisabledHooks(ctx, environment, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	wanted := canonicalWorktreeAdminPath(path)
	for _, worktree := range parseWorktreePorcelain(output) {
		if canonicalWorktreeAdminPath(worktree.Path) == wanted {
			return &worktree, nil
		}
	}
	return nil, nil
}

func canonicalWorktreeAdminPath(path string) string {
	cleaned := filepath.Clean(path)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(cleaned)); err == nil {
		return filepath.Join(parent, filepath.Base(cleaned))
	}
	return cleaned
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
