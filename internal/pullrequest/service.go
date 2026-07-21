package pullrequest

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type Provider interface {
	List(context.Context, Repository, string) ([]PullRequest, error)
	Get(context.Context, Repository, int) (PullRequest, error)
}

type WorkspaceBackend interface {
	ValidateImport(context.Context) error
	ListWorkspaces(context.Context) ([]Workspace, error)
	BranchExists(context.Context, string) (bool, error)
	EnsureRemote(context.Context, Repository) (string, error)
	Fetch(context.Context, string, string, string) (string, error)
	Create(context.Context, string, string) (Workspace, error)
	ConfigurePush(context.Context, Workspace, string, string) error
	Rollback(context.Context, Workspace) error
}

type Store interface {
	View(context.Context, func(map[string]Provenance) error) error
	Update(context.Context, func(map[string]Provenance) error) error
}

type Service struct {
	provider Provider
	backend  WorkspaceBackend
	store    Store
}

func NewService(provider Provider, backend WorkspaceBackend, store Store) *Service {
	return &Service{provider: provider, backend: backend, store: store}
}

func (s *Service) List(ctx context.Context, project Project, state string) ([]PullRequest, error) {
	repository, err := repositoryFromProject(project)
	if err != nil {
		return nil, err
	}
	project.Identity = repository.Identity
	prs, err := s.provider.List(ctx, repository, state)
	if err != nil {
		return nil, err
	}
	workspaces, err := s.backend.ListWorkspaces(ctx)
	if err != nil {
		return nil, NewError(CodeWorkspaceCreation, "failed to inspect project workspaces", false, err)
	}
	paths := make(map[string]Workspace, len(workspaces))
	for _, workspace := range workspaces {
		paths[workspace.Path] = workspace
	}
	records := make(map[string]Provenance)
	if err := s.store.View(ctx, func(current map[string]Provenance) error {
		for key, record := range current {
			records[key] = record
		}
		return nil
	}); err != nil {
		return nil, NewError(CodeWorkspaceCreation, "failed to read pull-request provenance", false, err)
	}
	for i := range prs {
		prs[i].Source.IsFork = !EqualRepositoryIdentity(prs[i].Source.Repository.Identity, prs[i].Repository.Identity)
		_, record, ok := findProvenance(records, prs[i])
		if ok && EqualRepositoryIdentity(record.Project.Identity, project.Identity) {
			if workspace, live := matchingProvenanceWorkspace(paths, record); live {
				prs[i].Imported = true
				prs[i].Workspace = &workspace
			}
		}
	}
	return prs, nil
}

func (s *Service) Import(ctx context.Context, project Project, selector string) (result ImportResult, err error) {
	repository, err := repositoryFromProject(project)
	if err != nil {
		return result, err
	}
	project.Identity = repository.Identity
	number, err := ParseSelector(selector, repository.Identity)
	if err != nil {
		return result, err
	}
	if err := s.backend.ValidateImport(ctx); err != nil {
		return result, err
	}
	pr, err := s.provider.Get(ctx, repository, number)
	if err != nil {
		return result, err
	}
	if !EqualRepositoryIdentity(pr.Repository.Identity, repository.Identity) {
		return result, NewError(CodeRepositoryMismatch,
			fmt.Sprintf("pull request belongs to %s, not project %s", pr.Repository.Identity, repository.Identity),
			false, nil)
	}
	if strings.TrimSpace(pr.Source.Repository.Identity) == "" || strings.TrimSpace(pr.Source.Name) == "" {
		return result, NewError(CodeInaccessibleHead, "pull-request head repository or branch is unavailable", false, nil)
	}
	pr.Source.IsFork = !EqualRepositoryIdentity(pr.Source.Repository.Identity, pr.Repository.Identity)
	branch := importBranchName(pr)
	var created *Workspace

	err = s.store.Update(ctx, func(records map[string]Provenance) error {
		workspaces, listErr := s.backend.ListWorkspaces(ctx)
		if listErr != nil {
			return NewError(CodeWorkspaceCreation, "failed to inspect project workspaces", false, listErr)
		}
		byPath := make(map[string]Workspace, len(workspaces))
		for _, workspace := range workspaces {
			byPath[workspace.Path] = workspace
		}
		recordKey, record, ok := findProvenance(records, pr)
		if ok {
			if !EqualRepositoryIdentity(record.Project.Identity, project.Identity) {
				return NewError(CodeConflict, "pull request is recorded for a different project", false, nil)
			}
			if workspace, live := matchingProvenanceWorkspace(byPath, record); live {
				if recordKey != pr.ID {
					delete(records, recordKey)
					record.PullRequestID = pr.ID
					record.Repository = NormalizeRepositoryIdentity(record.Repository)
					record.SourceRepo = NormalizeRepositoryIdentity(record.SourceRepo)
					record.Project.Identity = NormalizeRepositoryIdentity(record.Project.Identity)
					record.Workspace = workspace
					records[pr.ID] = record
				}
				result = ImportResult{Status: ImportExisting, PullRequest: pr, Project: project, Workspace: workspace}
				result.PullRequest.Imported = true
				result.PullRequest.Workspace = &result.Workspace
				return nil
			}
			if recordKey != pr.ID {
				delete(records, recordKey)
			}
		}

		exists, branchErr := s.backend.BranchExists(ctx, branch)
		if branchErr != nil {
			return NewError(CodeWorkspaceCreation, "failed to validate import branch", false, branchErr)
		}
		if exists {
			return NewError(CodeNamingConflict, fmt.Sprintf("branch %q already exists", branch), false, nil)
		}

		remote, remoteErr := s.backend.EnsureRemote(ctx, pr.Source.Repository)
		if remoteErr != nil {
			return AsError(remoteErr, CodeWorkspaceCreation, "failed to configure the pull-request Git remote")
		}
		fetchRef := fmt.Sprintf("refs/kwt/pull-requests/%s/%s/%d", pr.Repository.Owner, pr.Repository.Name, pr.Number)
		sha, fetchErr := s.backend.Fetch(ctx, remote, "refs/heads/"+pr.Source.Name, fetchRef)
		if fetchErr != nil {
			return AsError(fetchErr, CodeInaccessibleHead, "failed to fetch the pull-request head")
		}
		if !strings.EqualFold(strings.TrimSpace(sha), strings.TrimSpace(pr.HeadSHA)) {
			return NewError(CodeConflict, "pull-request head changed while it was being imported; retry", true, nil)
		}

		workspace, createErr := s.backend.Create(ctx, branch, fetchRef)
		if createErr != nil {
			return AsError(createErr, CodeWorkspaceCreation, "failed to create pull-request workspace")
		}
		created = &workspace
		if configErr := s.backend.ConfigurePush(ctx, workspace, remote, pr.Source.Name); configErr != nil {
			_ = s.backend.Rollback(ctx, workspace)
			created = nil
			return NewError(CodeWorkspaceCreation, "workspace created but push configuration failed; rolled it back", false, configErr)
		}

		newRecord := Provenance{
			PullRequestID: pr.ID, Provider: pr.Provider, Repository: pr.Repository.Identity,
			Number: pr.Number, URL: pr.URL, HeadSHA: pr.HeadSHA,
			SourceRepo: pr.Source.Repository.Identity, SourceBranch: pr.Source.Name,
			Project: project, Workspace: workspace,
		}
		records[pr.ID] = newRecord
		result = ImportResult{Status: ImportCreated, PullRequest: pr, Project: project, Workspace: workspace}
		result.PullRequest.Imported = true
		result.PullRequest.Workspace = &result.Workspace
		return nil
	})
	if err != nil {
		if created != nil {
			_ = s.backend.Rollback(context.WithoutCancel(ctx), *created)
			return ImportResult{}, NewError(CodeWorkspaceCreation,
				"workspace created but provenance could not be persisted; rolled it back", false, err)
		}
		return ImportResult{}, AsError(err, CodeWorkspaceCreation, "pull-request import failed")
	}
	return result, err
}

func findProvenance(records map[string]Provenance, pr PullRequest) (string, Provenance, bool) {
	if record, ok := records[pr.ID]; ok {
		return pr.ID, record, true
	}
	for key, record := range records {
		if record.Number != pr.Number || !EqualRepositoryIdentity(record.Repository, pr.Repository.Identity) {
			continue
		}
		if record.Provider != "" && !strings.EqualFold(record.Provider, pr.Provider) {
			continue
		}
		return key, record, true
	}
	return "", Provenance{}, false
}

func matchingProvenanceWorkspace(byPath map[string]Workspace, record Provenance) (Workspace, bool) {
	workspace, ok := byPath[record.Workspace.Path]
	if !ok || workspace.Branch != record.Workspace.Branch {
		return Workspace{}, false
	}
	return workspace, true
}

func repositoryFromProject(project Project) (Repository, error) {
	identity := NormalizeRepositoryIdentity(project.Identity)
	parts := strings.Split(identity, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return Repository{}, NewError(CodeUnsupportedProvider,
			fmt.Sprintf("project %q is not a supported GitHub repository identity", project.Identity), false, nil)
	}
	return Repository{
		Provider: "github", Identity: identity, Host: parts[0], Owner: parts[1], Name: parts[2],
	}, nil
}

func ParseSelector(selector, repository string) (int, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return 0, NewError(CodeInvalidSelector, "pull-request selector is empty", false, nil)
	}
	if number, err := strconv.Atoi(selector); err == nil && number > 0 {
		return number, nil
	}
	prefix := "github:" + NormalizeRepositoryIdentity(repository) + "#"
	if strings.HasPrefix(strings.ToLower(selector), prefix) {
		if number, err := strconv.Atoi(selector[len(prefix):]); err == nil && number > 0 {
			return number, nil
		}
	}
	parsed, err := url.Parse(selector)
	if err == nil && strings.EqualFold(parsed.Host, "github.com") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 4 && strings.EqualFold(parts[2], "pull") && EqualRepositoryIdentity("github.com/"+parts[0]+"/"+parts[1], repository) {
			if number, convertErr := strconv.Atoi(parts[3]); convertErr == nil && number > 0 {
				return number, nil
			}
		}
	}
	return 0, NewError(CodeInvalidSelector, "pull-request selector does not match the selected repository", false, nil)
}

func importBranchName(pr PullRequest) string {
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(pr.Source.Name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(out.String(), "-.")
	if slug == "" {
		slug = "head"
	}
	if len(slug) > 80 {
		slug = strings.TrimRight(slug[:80], "-.")
	}
	return fmt.Sprintf("pr-%d-%s", pr.Number, slug)
}
