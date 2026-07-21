package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/kwt/internal/config"
	gitadapter "go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/pullrequest"
	urlutil "go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/utils"
	"go.kenn.io/kwt/internal/worktree"
	"go.kenn.io/kwt/pkg/models"
)

type prService interface {
	List(context.Context, pullrequest.Project, string) ([]pullrequest.PullRequest, error)
	Import(context.Context, pullrequest.Project, string) (pullrequest.ImportResult, error)
}

var (
	prProject string
	prState   string
	prJSON    bool

	loadPRConfig          = config.Load
	loadPRTargetConfig    = config.LoadForTarget
	newPRService          = defaultNewPRService
	validatePRProjectRoot = defaultValidatePRProjectRoot
)

var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Discover and import pull requests as kwt workspaces",
	Args:  prNoArgs,
	// Pull-request commands select an explicit globally registered project.
	// A caller's cwd-local config must never alter a remote/SSH automation call.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := requireConfigInitialization(); err != nil {
			return writePRError(cmd, pullrequest.NewError(
				pullrequest.CodeWorkspaceCreation,
				"failed to initialize configuration",
				false,
				err,
			))
		}
		return nil
	},
}

var prListCmd = &cobra.Command{
	Use:   "list",
	Short: "List importable pull requests as JSON",
	Args:  prNoArgs,
	RunE:  runPRList,
}

var prImportCmd = &cobra.Command{
	Use:   "import <pull-request>",
	Short: "Import a pull request as a configured kwt workspace",
	Args:  prExactArgs(1),
	RunE:  runPRImport,
}

func init() {
	rootCmd.AddCommand(prCmd)
	prCmd.AddCommand(prListCmd, prImportCmd)
	prCmd.PersistentFlags().StringVar(&prProject, "project", "", "registered project identity, name, or path (defaults to current repository)")
	prCmd.PersistentFlags().BoolVar(&prJSON, "json", true, "emit the stable JSON automation contract")
	prListCmd.Flags().StringVar(&prState, "state", "open", "pull-request state: open, closed, or all")
	prCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return writePRError(cmd, pullrequest.NewError(pullrequest.CodeInvalidSelector, err.Error(), false, nil))
	})
}

func runPRList(cmd *cobra.Command, _ []string) error {
	if prState != "open" && prState != "closed" && prState != "all" {
		return writePRError(cmd, pullrequest.NewError(
			pullrequest.CodeInvalidSelector, "state must be open, closed, or all", false, nil))
	}
	project, err := preparePRProject()
	if err != nil {
		return writePRError(cmd, err)
	}
	service, err := preparePRService(cmd.Context(), project)
	if err != nil {
		return writePRError(cmd, err)
	}
	prs, err := service.List(cmd.Context(), project, prState)
	if err != nil {
		return writePRError(cmd, err)
	}
	return writePRJSON(cmd, struct {
		PullRequests []pullrequest.PullRequest `json:"pull_requests"`
	}{PullRequests: nonNilPullRequests(prs)})
}

func runPRImport(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return prExactArgs(1)(cmd, args)
	}
	project, err := preparePRProject()
	if err != nil {
		return writePRError(cmd, err)
	}
	if _, err := pullrequest.ParseSelector(args[0], project.Identity); err != nil {
		return writePRError(cmd, err)
	}
	service, err := preparePRService(cmd.Context(), project)
	if err != nil {
		return writePRError(cmd, err)
	}
	result, err := service.Import(cmd.Context(), project, args[0])
	if err != nil {
		return writePRError(cmd, err)
	}
	return writePRJSON(cmd, result)
}

func preparePRProject() (pullrequest.Project, error) {
	cfg, err := loadPRConfig()
	if err != nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeWorkspaceCreation, "failed to load kwt configuration", false, err)
	}
	project, err := resolvePRProject(cfg, prProject)
	if err != nil {
		return pullrequest.Project{}, err
	}
	return project, nil
}

func preparePRService(ctx context.Context, project pullrequest.Project) (prService, error) {
	project, err := validatePRProjectRoot(project)
	if err != nil {
		return nil, err
	}
	cfg, err := loadPRTargetConfig(project.Path, false)
	if err != nil {
		return nil, pullrequest.NewError(
			pullrequest.CodeWorkspaceCreation, "failed to load selected project configuration", false, err)
	}
	return newPRService(ctx, cfg, project)
}

func defaultNewPRService(ctx context.Context, cfg *models.Config, project pullrequest.Project) (prService, error) {
	provider, err := pullrequest.NewAuthenticatedGitHubProvider(ctx)
	if err != nil {
		return nil, err
	}
	g := gitadapter.New(project.Path)
	manager := worktree.New(g, cfg)
	backend := pullrequest.NewGitBackend(g, manager, project, pullrequest.WithFleetTokenEnvironment(cfg.Fleet.TokenEnv))
	return pullrequest.NewService(provider, backend, pullrequest.NewFileStore(prStorePath())), nil
}

func resolvePRProject(cfg *models.Config, selector string) (pullrequest.Project, error) {
	if cfg == nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "kwt project configuration is unavailable", false, nil)
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		g, err := gitadapter.NewFromCwd()
		if err != nil {
			return pullrequest.Project{}, pullrequest.NewError(
				pullrequest.CodeRepositoryMismatch, "--project is required outside a Git repository", false, err)
		}
		mainPath, err := g.GetMainRepositoryPath()
		if err != nil {
			return pullrequest.Project{}, pullrequest.NewError(
				pullrequest.CodeRepositoryMismatch, "failed to identify the current project", false, err)
		}
		for _, candidate := range cfg.Projects {
			if samePRPath(candidate.Path, mainPath) {
				return prProjectFromModel(candidate)
			}
		}
		info, err := worktree.RepositoryInfoWithProjects(g, cfg.Projects)
		if err != nil {
			return pullrequest.Project{}, pullrequest.NewError(
				pullrequest.CodeRepositoryMismatch, "current repository has no stable provider identity", false, err)
		}
		return validatePRProject(pullrequest.Project{Identity: info.FullPath, Name: info.Repository, Path: mainPath})
	}

	for _, candidate := range cfg.Projects {
		identity := publishableProjectRepository(candidate)
		if pullrequest.EqualRepositoryIdentity(selector, identity) || samePRPath(selector, candidate.Path) {
			candidate.Repository = identity
			return prProjectFromModel(candidate)
		}
	}
	var nameMatches []models.Project
	for _, candidate := range cfg.Projects {
		if strings.EqualFold(selector, candidate.Name) {
			nameMatches = append(nameMatches, candidate)
		}
	}
	if len(nameMatches) == 1 {
		candidate := nameMatches[0]
		candidate.Repository = publishableProjectRepository(candidate)
		return prProjectFromModel(candidate)
	}
	if len(nameMatches) > 1 {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch,
			fmt.Sprintf("project name %q is ambiguous; select by repository identity or path", selector), false, nil)
	}
	return pullrequest.Project{}, pullrequest.NewError(
		pullrequest.CodeRepositoryMismatch, fmt.Sprintf("no kwt-managed project matches %q", selector), false, nil)
}

func prProjectFromModel(project models.Project) (pullrequest.Project, error) {
	identity := publishableProjectRepository(project)
	return validatePRProject(pullrequest.Project{Identity: identity, Name: project.Name, Path: project.Path})
}

func validatePRProject(project pullrequest.Project) (pullrequest.Project, error) {
	info, ok := urlutil.CanonicalRepositoryInfo(project.Identity)
	if !ok || !strings.EqualFold(info.Host, "github.com") {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeUnsupportedProvider,
			fmt.Sprintf("project %q is not a supported github.com repository", project.Identity), false, nil)
	}
	project.Identity = pullrequest.NormalizeRepositoryIdentity(info.FullPath)
	if strings.TrimSpace(project.Path) == "" {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project has no repository path", false, nil)
	}
	if strings.TrimSpace(project.Name) == "" {
		project.Name = info.Repository
	}
	return project, nil
}

func defaultValidatePRProjectRoot(project pullrequest.Project) (pullrequest.Project, error) {
	path := strings.TrimSpace(project.Path)
	if path == "" {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project has no repository path", false, nil)
	}
	if !filepath.IsAbs(path) {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project path must be absolute", false, nil)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project path is invalid", false, err)
	}
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project path is unavailable", false, err)
	}
	info, err := os.Stat(canonicalPath)
	if err != nil || !info.IsDir() {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project path is not a directory", false, err)
	}
	mainPath, err := gitadapter.New(canonicalPath).GetMainRepositoryPath()
	if err != nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project path is not a Git repository", false, err)
	}
	canonicalMain, err := filepath.EvalSymlinks(mainPath)
	if err != nil {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch, "selected project main repository is unavailable", false, err)
	}
	if filepath.Clean(canonicalPath) != filepath.Clean(canonicalMain) {
		return pullrequest.Project{}, pullrequest.NewError(
			pullrequest.CodeRepositoryMismatch,
			"selected project path is not the main repository root", false, nil)
	}
	project.Path = canonicalPath
	return project, nil
}

func prNoArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return writePRError(cmd, pullrequest.NewError(
		pullrequest.CodeInvalidSelector, "this command does not accept positional arguments", false, nil))
}

func prExactArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		return writePRError(cmd, pullrequest.NewError(
			pullrequest.CodeInvalidSelector, fmt.Sprintf("expected %d pull-request selector, received %d", count, len(args)), false, nil))
	}
}

func samePRPath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = resolved
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func prStorePath() string {
	if kwtHome := strings.TrimSpace(os.Getenv("KWT_HOME")); kwtHome != "" {
		if expanded, err := utils.ExpandPath(kwtHome); err == nil {
			return filepath.Join(expanded, "pull-requests.json")
		}
		return filepath.Join(kwtHome, "pull-requests.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "kwt", "pull-requests.json")
	}
	return filepath.Join(home, ".config", "kwt", "pull-requests.json")
}

func nonNilPullRequests(prs []pullrequest.PullRequest) []pullrequest.PullRequest {
	if prs == nil {
		return []pullrequest.PullRequest{}
	}
	return prs
}

func writePRJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type prCommandError struct {
	err *pullrequest.Error
}

func (e *prCommandError) Error() string { return e.err.Error() }
func (e *prCommandError) Unwrap() error { return e.err }
func (e *prCommandError) ExitCode() int { return prExitCode(e.err.Code) }

func writePRError(cmd *cobra.Command, err error) error {
	typed := pullrequest.AsError(err, pullrequest.CodeWorkspaceCreation, "pull-request operation failed")
	cmd.Root().SilenceUsage = true
	cmd.Root().SilenceErrors = true
	_ = writePRJSON(cmd, pullrequest.ErrorEnvelope{Error: typed})
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "kwt pr: %s: %s\n", typed.Code, typed.Message)
	return &prCommandError{err: typed}
}

func prExitCode(code pullrequest.ErrorCode) int {
	switch code {
	case pullrequest.CodeAuthentication:
		return 3
	case pullrequest.CodeRepositoryMismatch, pullrequest.CodeUnsupportedProvider:
		return 4
	case pullrequest.CodeInvalidSelector:
		return 2
	case pullrequest.CodeNotFound:
		return 5
	case pullrequest.CodeInaccessibleHead:
		return 6
	case pullrequest.CodeNamingConflict:
		return 7
	case pullrequest.CodeNetwork:
		return 8
	case pullrequest.CodeWorkspaceCreation:
		return 9
	case pullrequest.CodeMalformedResponse:
		return 10
	case pullrequest.CodeConflict:
		return 11
	case pullrequest.CodeUnsupportedGitVersion:
		return 12
	default:
		return 1
	}
}
