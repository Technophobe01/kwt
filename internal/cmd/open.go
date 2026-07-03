package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.kenn.io/kwt/internal/config"
	"go.kenn.io/kwt/internal/discovery"
	"go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/tmux"
	"go.kenn.io/kwt/pkg/models"
)

var (
	openLayout       string
	openSelectLayout bool
)

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open a worktree workspace from any repository",
	Long: `Fuzzy-pick a worktree across all repositories in the configured base
directory and attach to its tmux workspace, creating the workspace with the
resolved layout if it does not yet exist.`,
	Example: `  # Pick a worktree and open its workspace
  kwt open

  # Force a specific layout
  kwt open --layout focus

  # Fuzzy-pick the layout too
  kwt open --select-layout`,
	// Isolation: open must not merge the caller's cwd .kwt.toml. Overriding the
	// root PersistentPreRunE with a no-op keeps the global config pristine.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().StringVar(&openLayout, "layout", "", "Workspace layout preset to launch")
	openCmd.Flags().BoolVarP(&openSelectLayout, "select-layout", "L", false,
		"Fuzzy-pick a workspace layout")
	openCmd.MarkFlagsMutuallyExclusive("layout", "select-layout")
}

func runOpen(cmd *cobra.Command, args []string) error {
	return ExecuteWithContext(false, func(ctx *CommandContext) error {
		if err := tmux.ValidateLayouts(ctx.Config.Layouts, ctx.Config.Agents); err != nil {
			return err
		}

		entries, err := discovery.DiscoverGlobalWorktrees(ctx.Config.Worktree.BaseDir)
		if err != nil {
			return fmt.Errorf("failed to discover worktrees: %w", err)
		}
		if len(entries) == 0 {
			ctx.Printer.PrintInfo("No worktrees found in " + ctx.Config.Worktree.BaseDir)
			return nil
		}

		finder := ctx.GetGlobalFinder()
		worktrees := discovery.ConvertToWorktreeModels(entries, false)
		selected, err := finder.SelectWorktree(worktrees)
		if err != nil {
			return fmt.Errorf("selection cancelled: %w", err)
		}

		entry := findEntryByPath(entries, selected.Path)
		if entry == nil || entry.RepositoryInfo == nil {
			return fmt.Errorf("could not resolve repository info for %s", selected.Path)
		}

		// Only resolve the target repo's default layout (which requires
		// finding its root and loading, and possibly trust-prompting for, its
		// .kwt.toml) when neither flag already determines the layout. This
		// avoids trust-prompting on, or failing over, a malformed target
		// config whose default would be ignored anyway.
		targetDefault := ""
		if shouldLoadTargetDefault(openLayout, openSelectLayout) {
			repoRoot, err := git.New(entry.Path).GetMainRepositoryPath()
			if err != nil {
				return fmt.Errorf("failed to find repository root: %w", err)
			}
			targetDefault, err = config.LoadRepoLayoutDefault(repoRoot, config.StdinInteractive())
			if err != nil {
				return err
			}
		}

		layout, err := tmux.ResolveLayout(ctx.Config.Layouts, openLayout, openSelectLayout, targetDefault,
			func(ls []models.Layout) (models.Layout, error) {
				sel, err := finder.SelectLayout(ls)
				if err != nil {
					return models.Layout{}, err
				}
				return *sel, nil
			})
		if err != nil {
			return err
		}
		layout, err = tmux.ResolvePaneCommands(layout, ctx.Config.Agents)
		if err != nil {
			return err
		}

		session := tmux.WorkspaceSessionName(entry.RepositoryInfo, entry.Branch, entry.Path)
		runner := tmux.NewWorkspaceRunner(tmux.NewTmuxCommand(""))
		return runner.EnsureAndAttach(
			context.Background(), session, entry.Path, layout, os.Getenv("TMUX") != "",
		)
	})(cmd, args)
}

// shouldLoadTargetDefault reports whether kwt open must consult the target
// repo's .kwt.toml layouts.default: only when no explicit layout flag was
// given (an explicit --layout/--select-layout overrides the target default
// anyway).
func shouldLoadTargetDefault(layoutFlag string, selectLayout bool) bool {
	return layoutFlag == "" && !selectLayout
}

// findEntryByPath returns the discovery entry whose Path matches, or nil.
func findEntryByPath(
	entries []*discovery.GlobalWorktreeEntry, path string,
) *discovery.GlobalWorktreeEntry {
	for _, e := range entries {
		if e.Path == path {
			return e
		}
	}
	return nil
}
