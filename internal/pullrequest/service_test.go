package pullrequest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testProject() Project {
	return Project{
		Identity: "github.com/acme/widget",
		Name:     "widget",
		Path:     "/repos/widget",
	}
}

func testPR(number int, fork bool) PullRequest {
	sourceRepo := Repository{
		Provider: "github",
		Identity: "github.com/acme/widget",
		Host:     "github.com",
		Owner:    "acme",
		Name:     "widget",
		CloneURL: "https://github.com/acme/widget.git",
	}
	if fork {
		sourceRepo.Identity = "github.com/octocat/widget"
		sourceRepo.Owner = "octocat"
		sourceRepo.CloneURL = "https://github.com/octocat/widget.git"
	}
	return PullRequest{
		ID:       "github:github.com/acme/widget#" + itoa(number),
		Provider: "github",
		Repository: Repository{
			Provider: "github",
			Identity: "github.com/acme/widget",
			Host:     "github.com",
			Owner:    "acme",
			Name:     "widget",
			CloneURL: "https://github.com/acme/widget.git",
		},
		Number: number,
		URL:    "https://github.com/acme/widget/pull/" + itoa(number),
		Title:  "Improve widgets",
		Author: "octocat",
		Source: Branch{Repository: sourceRepo, Name: "feature/widgets"},
		Target: Branch{Repository: Repository{
			Provider: "github", Identity: "github.com/acme/widget", Host: "github.com",
			Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
		}, Name: "main"},
		State:   "open",
		HeadSHA: "0123456789abcdef0123456789abcdef01234567",
	}
}

type fakeProvider struct {
	prs     []PullRequest
	listErr error
	getErr  error
}

func (f *fakeProvider) List(context.Context, Repository, string) ([]PullRequest, error) {
	return append([]PullRequest(nil), f.prs...), f.listErr
}

func (f *fakeProvider) Get(_ context.Context, _ Repository, number int) (PullRequest, error) {
	if f.getErr != nil {
		return PullRequest{}, f.getErr
	}
	for _, pr := range f.prs {
		if pr.Number == number {
			return pr, nil
		}
	}
	return PullRequest{}, NewError(CodeNotFound, "pull request not found", false, nil)
}

type fakeWorkspaceBackend struct {
	mu              sync.Mutex
	workspaces      []Workspace
	remotes         map[string]string
	fetchedRemote   string
	fetchedRef      string
	fetchedSHA      string
	createdBranch   string
	createCalls     int
	createErr       error
	configureErr    error
	ensureRemoteErr error
}

func newFakeBackend() *fakeWorkspaceBackend {
	return &fakeWorkspaceBackend{
		remotes: map[string]string{
			"origin": "github.com/acme/widget",
		},
		fetchedSHA: "0123456789abcdef0123456789abcdef01234567",
	}
}

func (f *fakeWorkspaceBackend) ListWorkspaces(context.Context) ([]Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Workspace(nil), f.workspaces...), nil
}

func (f *fakeWorkspaceBackend) BranchExists(_ context.Context, branch string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, workspace := range f.workspaces {
		if workspace.Branch == branch {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeWorkspaceBackend) EnsureRemote(_ context.Context, repo Repository) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureRemoteErr != nil {
		return "", f.ensureRemoteErr
	}
	for name, identity := range f.remotes {
		if identity == repo.Identity {
			return name, nil
		}
	}
	name := "kwt-pr-" + repo.Owner
	f.remotes[name] = repo.Identity
	return name, nil
}

func (f *fakeWorkspaceBackend) Fetch(_ context.Context, remote, sourceRef, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchedRemote = remote
	f.fetchedRef = sourceRef
	if f.fetchedSHA == "" {
		return "", NewError(CodeInaccessibleHead, "head ref is unavailable", false, nil)
	}
	return f.fetchedSHA, nil
}

func (f *fakeWorkspaceBackend) Create(_ context.Context, branch, _ string) (Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.createdBranch = branch
	if f.createErr != nil {
		return Workspace{}, f.createErr
	}
	workspace := Workspace{
		ID:          "github.com/acme/widget:" + branch,
		Repository:  "github.com/acme/widget",
		Branch:      branch,
		Path:        "/worktrees/widget/" + branch,
		State:       "ready",
		SessionName: "kwt-workspace-github-com-acme-widget-" + branch,
	}
	f.workspaces = append(f.workspaces, workspace)
	return workspace, nil
}

func (f *fakeWorkspaceBackend) ConfigurePush(_ context.Context, _ Workspace, remote, sourceBranch string) error {
	if f.configureErr != nil {
		return f.configureErr
	}
	if remote == "" || sourceBranch == "" {
		return errors.New("missing push configuration")
	}
	return nil
}

func (f *fakeWorkspaceBackend) Rollback(_ context.Context, workspace Workspace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.workspaces {
		if f.workspaces[i].Path == workspace.Path {
			f.workspaces = append(f.workspaces[:i], f.workspaces[i+1:]...)
			break
		}
	}
	return nil
}

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Provenance
}

type commitFailStore struct {
	*memoryStore
}

func (s *commitFailStore) Update(ctx context.Context, fn func(map[string]Provenance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := cloneRecords(s.records)
	if err := fn(copy); err != nil {
		return err
	}
	return errors.New("disk full")
}

func newMemoryStore() *memoryStore { return &memoryStore{records: make(map[string]Provenance)} }

func (s *memoryStore) View(_ context.Context, fn func(map[string]Provenance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(cloneRecords(s.records))
}

func (s *memoryStore) Update(_ context.Context, fn func(map[string]Provenance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := cloneRecords(s.records)
	if err := fn(copy); err != nil {
		return err
	}
	s.records = copy
	return nil
}

func cloneRecords(records map[string]Provenance) map[string]Provenance {
	copy := make(map[string]Provenance, len(records))
	for key, value := range records {
		copy[key] = value
	}
	return copy
}

func newTestService(provider Provider, backend WorkspaceBackend, store Store) *Service {
	return NewService(provider, backend, store)
}

func TestListReturnsSameRepoForkAndDraftDetails(t *testing.T) {
	same := testPR(11, false)
	fork := testPR(12, true)
	draft := testPR(13, false)
	draft.Draft = true
	service := newTestService(&fakeProvider{prs: []PullRequest{same, fork, draft}}, newFakeBackend(), newMemoryStore())

	got, err := service.List(context.Background(), testProject(), "open")

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "github.com/acme/widget", got[0].Repository.Identity)
	assert.False(t, got[0].Source.IsFork)
	assert.True(t, got[1].Source.IsFork)
	assert.Equal(t, "github.com/octocat/widget", got[1].Source.Repository.Identity)
	assert.True(t, got[2].Draft)
	assert.False(t, got[0].Imported)
}

func TestListMarksExistingImport(t *testing.T) {
	pr := testPR(21, false)
	backend := newFakeBackend()
	workspace := Workspace{ID: "ws-21", Repository: testProject().Identity, Branch: "pr-21-feature-widgets", Path: "/worktrees/21", State: "ready", SessionName: "session-21"}
	backend.workspaces = []Workspace{workspace}
	store := newMemoryStore()
	store.records[pr.ID] = Provenance{PullRequestID: pr.ID, Project: testProject(), Workspace: workspace, HeadSHA: pr.HeadSHA}
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, store)

	got, err := service.List(context.Background(), testProject(), "open")

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].Imported)
	assert.Equal(t, &workspace, got[0].Workspace)
}

func TestImportSameRepositoryUsesMatchingRemoteAndCanonicalName(t *testing.T) {
	pr := testPR(31, false)
	backend := newFakeBackend()
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	result, err := service.Import(context.Background(), testProject(), "31")

	require.NoError(t, err)
	assert.Equal(t, ImportCreated, result.Status)
	assert.Equal(t, "origin", backend.fetchedRemote)
	assert.Equal(t, "refs/heads/feature/widgets", backend.fetchedRef)
	assert.Equal(t, "pr-31-feature-widgets", result.Workspace.Branch)
	assert.Equal(t, testProject().Identity, result.Workspace.Repository)
}

func TestImportForkCreatesAndUsesForkRemote(t *testing.T) {
	pr := testPR(32, true)
	backend := newFakeBackend()
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	result, err := service.Import(context.Background(), testProject(), pr.URL)

	require.NoError(t, err)
	assert.Equal(t, ImportCreated, result.Status)
	assert.Equal(t, "kwt-pr-octocat", backend.fetchedRemote)
	assert.Equal(t, "github.com/octocat/widget", backend.remotes["kwt-pr-octocat"])
}

func TestImportIsIdempotent(t *testing.T) {
	pr := testPR(41, false)
	backend := newFakeBackend()
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	first, err := service.Import(context.Background(), testProject(), pr.ID)
	require.NoError(t, err)
	second, err := service.Import(context.Background(), testProject(), pr.ID)
	require.NoError(t, err)

	assert.Equal(t, ImportCreated, first.Status)
	assert.Equal(t, ImportExisting, second.Status)
	assert.Equal(t, first.Workspace, second.Workspace)
	assert.Equal(t, 1, backend.createCalls)
}

func TestConcurrentImportConverges(t *testing.T) {
	pr := testPR(42, false)
	backend := newFakeBackend()
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	results := make(chan ImportResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Import(context.Background(), testProject(), "42")
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var statuses []ImportStatus
	for result := range results {
		statuses = append(statuses, result.Status)
	}
	assert.ElementsMatch(t, []ImportStatus{ImportCreated, ImportExisting}, statuses)
	assert.Equal(t, 1, backend.createCalls)
}

func TestImportReportsNamingConflict(t *testing.T) {
	pr := testPR(51, false)
	backend := newFakeBackend()
	backend.workspaces = []Workspace{{Branch: "pr-51-feature-widgets", Path: "/unrelated"}}
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	_, err := service.Import(context.Background(), testProject(), "51")

	assertErrorCode(t, err, CodeNamingConflict)
}

func TestImportReportsUnavailableHead(t *testing.T) {
	pr := testPR(52, true)
	backend := newFakeBackend()
	backend.fetchedSHA = ""
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	_, err := service.Import(context.Background(), testProject(), "52")

	assertErrorCode(t, err, CodeInaccessibleHead)
	assert.Empty(t, backend.workspaces)
}

func TestImportRejectsRepositoryMismatch(t *testing.T) {
	pr := testPR(53, false)
	pr.Repository.Identity = "github.com/other/widget"
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, newFakeBackend(), newMemoryStore())

	_, err := service.Import(context.Background(), testProject(), "53")

	assertErrorCode(t, err, CodeRepositoryMismatch)
}

func TestImportPropagatesTypedProviderFailures(t *testing.T) {
	for _, code := range []ErrorCode{CodeAuthentication, CodeNetwork, CodeMalformedResponse, CodeNotFound} {
		t.Run(string(code), func(t *testing.T) {
			providerErr := NewError(code, "provider failed", code == CodeNetwork, errors.New("cause"))
			service := newTestService(&fakeProvider{getErr: providerErr}, newFakeBackend(), newMemoryStore())

			_, err := service.Import(context.Background(), testProject(), "99")

			assertErrorCode(t, err, code)
		})
	}
}

func TestImportUsesMatchingRemoteAmongMultipleRemotes(t *testing.T) {
	pr := testPR(54, true)
	backend := newFakeBackend()
	backend.remotes = map[string]string{
		"origin":   "github.com/acme/widget",
		"personal": "github.com/octocat/widget",
		"mirror":   "github.com/mirror/widget",
	}
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	_, err := service.Import(context.Background(), testProject(), "54")

	require.NoError(t, err)
	assert.Equal(t, "personal", backend.fetchedRemote)
}

func TestImportWrapsCreationAndPushConfigurationFailures(t *testing.T) {
	pr := testPR(55, false)
	for _, tc := range []struct {
		name      string
		configure bool
	}{
		{name: "creation"},
		{name: "push configuration", configure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newFakeBackend()
			if tc.configure {
				backend.configureErr = errors.New("config failed")
			} else {
				backend.createErr = errors.New("create failed")
			}
			service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

			_, err := service.Import(context.Background(), testProject(), "55")

			assertErrorCode(t, err, CodeWorkspaceCreation)
			assert.Empty(t, backend.workspaces, "a partially configured workspace must be rolled back")
		})
	}
}

func TestImportPreservesTypedNamingFailureFromWorkspaceCreation(t *testing.T) {
	pr := testPR(56, false)
	backend := newFakeBackend()
	backend.createErr = NewError(CodeNamingConflict, "workspace path already exists", false, nil)
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, newMemoryStore())

	_, err := service.Import(context.Background(), testProject(), "56")

	assertErrorCode(t, err, CodeNamingConflict)
}

func TestImportRollsBackWhenProvenanceCannotBeCommitted(t *testing.T) {
	pr := testPR(57, false)
	backend := newFakeBackend()
	store := &commitFailStore{memoryStore: newMemoryStore()}
	service := newTestService(&fakeProvider{prs: []PullRequest{pr}}, backend, store)

	_, err := service.Import(context.Background(), testProject(), "57")

	assertErrorCode(t, err, CodeWorkspaceCreation)
	assert.Empty(t, backend.workspaces)
}

func assertErrorCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var typed *Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, want, typed.Code)
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}
