package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/kwt/internal/config"
	"go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/table"
	"go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/worktree"
	"go.kenn.io/kwt/pkg/models"
)

var (
	projectsJSON       bool
	loadProjectsConfig = config.Load
)

var projectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List registered project repositories",
	Long: `List repositories kwt has registered for cross-project discovery.

Registered projects hold main-repository paths that may live outside the
configured worktree base directory. Use --json for a machine-readable surface
that external automation can consume without parsing the config file.`,
	Args: cobra.NoArgs,
	// Isolation: projects is a global registry surface and must not merge the
	// caller's cwd .kwt.toml. The command still propagates global config
	// initialization failures through Cobra.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return requireConfigInitialization() },
	RunE:              runProjects,
}

func init() {
	rootCmd.AddCommand(projectsCmd)
	projectsCmd.Flags().BoolVar(&projectsJSON, "json", false, "Output in JSON format")
}

func runProjects(cmd *cobra.Command, args []string) error {
	cfg, err := loadProjectsConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	projects := canonicalizeProjectIdentities(cfg.Projects)

	if projectsJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(projects)
	}

	if len(projects) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no projects registered")
		return nil
	}

	t := table.New().SetOutput(cmd.OutOrStdout()).Headers("NAME", "REPOSITORY", "PATH", "LAST TOUCHED")
	for _, project := range projects {
		t.Row(project.Name, project.Repository, project.Path, project.LastTouched)
	}
	return t.Println()
}

// canonicalizeProjectIdentities returns a copy of projects with every
// Repository value resolved through the canonical identity bar, so projects
// output (JSON and table) emits the same identities kwt list --json reports.
func canonicalizeProjectIdentities(projects []models.Project) []models.Project {
	out := make([]models.Project, len(projects))
	for i, project := range projects {
		out[i] = project
		out[i].Repository = publishableProjectRepository(project)
	}
	return out
}

// publishableProjectRepository resolves the repository identity projects
// emits for a registry entry: path-backed entries resolve through the same
// registered-identity resolver every other surface uses; otherwise a stored
// canonical identity is authoritative, and path fallbacks resolve through the
// canonical local-path identity. A raw unvalidated registry value is never
// emitted.
func publishableProjectRepository(project models.Project) string {
	if project.Path != "" {
		// The same registered-identity precedence kwt list uses, applied to
		// this single entry.
		info, err := worktree.RepositoryInfoWithProjects(
			git.New(project.Path), []models.Project{project})
		if err == nil {
			return info.FullPath
		}
	}
	if identity, ok := url.CanonicalRepositoryIdentity(project.Repository); ok {
		return identity
	}
	if project.Path != "" {
		if info, err := worktree.RepositoryInfoFromLocalPath(project.Path); err == nil {
			return info.FullPath
		}
	}
	if stored := strings.TrimSpace(project.Repository); url.IsLocalFallbackIdentity(stored) {
		return stored
	}
	return ""
}
