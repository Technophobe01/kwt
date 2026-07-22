package pullrequest

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

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
	expectedRemoteURLsMu      sync.RWMutex
	expectedRemoteURLs        map[string]string
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

func NewGitBackend(g *gitadapter.Git, manager *worktree.Manager, project Project, options ...GitBackendOption) *GitBackend {
	backend := &GitBackend{git: g, manager: manager, project: project, expectedRemoteURLs: make(map[string]string)}
	for _, option := range options {
		option(backend)
	}
	blockedEnvironment := append([]string{backend.fleetTokenEnv}, backend.fleetTokenFileEnvironment...)
	backend.setupEnvironment = gitadapter.NonInteractiveEnvironment(SafeSetupEnvironment(os.Environ(), blockedEnvironment...))
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
	output, err := b.git.RunWithContext(ctx, "version")
	if err != nil {
		return NewError(CodeUnsupportedGitVersion, "failed to determine Git version", false, err)
	}
	if !supportsWorktreeConfig(output) {
		return NewError(CodeUnsupportedGitVersion, "pull-request import requires Git 2.20 or newer", false, nil)
	}
	return b.validateImportConfigurationAt(ctx, "")
}

func (b *GitBackend) validateImportConfigurationAt(ctx context.Context, worktreePath string) error {
	configOutput, err := b.runImportGitAt(ctx, worktreePath, "config", "--includes", "--null", "--list")
	if err != nil {
		return NewError(CodeWorkspaceCreation, "failed to inspect effective Git configuration", false, err)
	}
	if gitConfigHasEmbeddedRemoteCredentials(configOutput) {
		return embeddedRemoteCredentialsError()
	}
	if gitConfigHasCustomReceivePack(configOutput) {
		return NewError(CodeWorkspaceCreation,
			"pull-request import does not allow custom Git receive-pack commands", false, nil)
	}
	remotes, err := b.runImportGitAt(ctx, worktreePath, "remote")
	if err != nil {
		return NewError(CodeWorkspaceCreation, "failed to enumerate effective Git remotes", false, err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(remotes), "\n") {
		remote := strings.TrimSpace(line)
		if remote == "" {
			continue
		}
		for _, args := range [][]string{
			{"remote", "get-url", "--all", remote},
			{"remote", "get-url", "--all", "--push", remote},
		} {
			urls, urlErr := b.runImportGitAt(ctx, worktreePath, args...)
			if urlErr != nil {
				return NewError(CodeWorkspaceCreation, "failed to inspect effective Git remote URLs", false, urlErr)
			}
			if remoteURLListHasEmbeddedCredentials(urls) {
				return embeddedRemoteCredentialsError()
			}
		}
	}
	return nil
}

func (b *GitBackend) runImportGit(ctx context.Context, args ...string) (string, error) {
	return b.git.RunWithEnvironmentAndDisabledHooks(ctx, b.setupEnvironment, args...)
}

func (b *GitBackend) runImportGitAt(ctx context.Context, worktreePath string, args ...string) (string, error) {
	if strings.TrimSpace(worktreePath) != "" {
		args = append([]string{"-C", worktreePath}, args...)
	}
	return b.runImportGit(ctx, args...)
}

func (b *GitBackend) validateWorktreeConfiguration(ctx context.Context, worktreePath string) error {
	if err := b.validateImportConfigurationAt(ctx, worktreePath); err != nil {
		return err
	}
	mainConfig, err := b.effectiveConfigRecords(ctx, "")
	if err != nil {
		return NewError(CodeWorkspaceCreation, "failed to inspect main Git configuration", false, err)
	}
	worktreeConfig, err := b.effectiveConfigRecords(ctx, worktreePath)
	if err != nil {
		return NewError(CodeWorkspaceCreation, "failed to inspect pull-request worktree Git configuration", false, err)
	}
	if !slices.Equal(mainConfig, worktreeConfig) {
		return NewError(CodeAuthentication,
			"pull-request worktree activates different Git configuration; remove branch- or worktree-conditional includes",
			false, nil)
	}
	return nil
}

func (b *GitBackend) effectiveConfigRecords(ctx context.Context, worktreePath string) ([]string, error) {
	output, err := b.runImportGitAt(ctx, worktreePath, "config", "--includes", "--null", "--list")
	if err != nil {
		return nil, err
	}
	records := make([]string, 0)
	for record := range strings.SplitSeq(output, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		if strings.EqualFold(strings.TrimSpace(key), "core.hookspath") || record == "" {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func embeddedRemoteCredentialsError() *Error {
	return NewError(CodeAuthentication,
		"pull-request import requires credential-free Git remote URLs; use a credential helper or SSH agent",
		false, nil)
}

func gitConfigHasEmbeddedRemoteCredentials(output string) bool {
	for record := range strings.SplitSeq(output, "\x00") {
		key, value, found := strings.Cut(record, "\n")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if !strings.HasPrefix(key, "remote.") ||
			(!strings.HasSuffix(key, ".url") && !strings.HasSuffix(key, ".pushurl")) {
			continue
		}
		if remoteURLHasEmbeddedCredentials(strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func gitConfigHasCustomReceivePack(output string) bool {
	for record := range strings.SplitSeq(output, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		key = strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".receivepack") {
			return true
		}
	}
	return false
}

func remoteURLListHasEmbeddedCredentials(output string) bool {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if remoteURLHasEmbeddedCredentials(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

func remoteURLHasEmbeddedCredentials(remoteURL string) bool {
	if strings.ContainsAny(remoteURL, "?#") || urlutil.IsRemoteHelperURL(remoteURL) {
		return true
	}
	if strings.Contains(remoteURL, "://") {
		parsed, err := neturl.Parse(remoteURL)
		if err != nil {
			return true
		}
		if parsed.User != nil {
			scheme := strings.ToLower(parsed.Scheme)
			if scheme == "ssh" || scheme == "git+ssh" {
				_, hasPassword := parsed.User.Password()
				return hasPassword
			}
			return true
		}
	}
	at := strings.IndexByte(remoteURL, '@')
	if at < 0 {
		return false
	}
	return strings.Contains(remoteURL[:at], ":") && strings.Contains(remoteURL[at+1:], ":")
}

var gitVersionPattern = regexp.MustCompile(`(?i)git version (\d+)\.(\d+)(?:\.(\d+))?`)

func supportsWorktreeConfig(output string) bool {
	match := gitVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) == 0 {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major > 2 || major == 2 && minor >= 20
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
	output, err := b.runImportGit(ctx, "remote")
	if err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to list Git remotes", false, err)
	}
	existing := make(map[string]bool)
	remoteNames := make([]string, 0)
	matchingRemote := ""
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		existing[name] = true
		remoteNames = append(remoteNames, name)
	}
	projectSSHURL := b.projectPushSSHURL(ctx, existing)
	remoteURL := strings.TrimSpace(repository.CloneURL)
	if projectSSHURL != "" {
		var buildErr error
		remoteURL, buildErr = forkSSHURL(projectSSHURL, repository)
		if buildErr != nil {
			return "", NewError(CodeWorkspaceCreation,
				"failed to preserve the project's SSH transport for the pull-request remote", false, buildErr)
		}
	}
	if remoteURL == "" {
		return "", NewError(CodeInaccessibleHead, "pull-request head repository has no clone URL", false, nil)
	}
	for _, name := range remoteNames {
		fetchURLs, fetchErr := b.runImportGit(ctx, "remote", "get-url", "--all", name)
		pushURLs, pushErr := b.runImportGit(ctx, "remote", "get-url", "--all", "--push", name)
		if fetchErr != nil || pushErr != nil {
			continue
		}
		if remoteURLListHasEmbeddedCredentials(fetchURLs) || remoteURLListHasEmbeddedCredentials(pushURLs) {
			continue
		}
		fetchURL, singleFetch := singleRemoteURL(fetchURLs)
		fetchIdentity, fetchOK := urlutil.CanonicalRepositoryIdentityFromRemote(fetchURL)
		fetchMatches := singleFetch && fetchOK && EqualRepositoryIdentity(fetchIdentity, repository.Identity)
		pushMatches := singleRemoteURLMatches(pushURLs, repository.Identity)
		if projectSSHURL != "" {
			fetchMatches = singleFetch && remoteURLsEqual(fetchURL, remoteURL)
			pushMatches = singleRemoteURLEquals(pushURLs, remoteURL)
		}
		if fetchMatches && matchingRemote == "" && pushMatches &&
			!b.remoteHasCustomPushRefspec(ctx, name) {
			matchingRemote = name
		}
	}
	if matchingRemote != "" {
		if projectSSHURL != "" {
			b.rememberExpectedRemoteURL(matchingRemote, remoteURL)
		}
		return matchingRemote, nil
	}
	base := "kwt-pr-" + sanitizeRemoteComponent(repository.Owner)
	name := base
	for suffix := 2; existing[name]; suffix++ {
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	if _, err := b.runImportGit(ctx, "remote", "add", name, remoteURL); err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to add pull-request Git remote", false, err)
	}
	b.rememberExpectedRemoteURL(name, remoteURL)
	if validateErr := b.validateEffectiveRemote(ctx, name, repository.Identity); validateErr != nil {
		b.forgetExpectedRemoteURL(name)
		_, removeErr := b.runImportGit(context.WithoutCancel(ctx), "remote", "remove", name)
		if removeErr != nil {
			return "", NewError(CodeWorkspaceCreation,
				"new pull-request Git remote was unsafe and could not be removed", false,
				errors.Join(validateErr, removeErr))
		}
		return "", validateErr
	}
	return name, nil
}

func (b *GitBackend) projectPushSSHURL(ctx context.Context, remotes map[string]bool) string {
	selected := ""
	branch, branchErr := b.runImportGit(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr == nil {
		branch = strings.TrimSpace(branch)
		selected = b.configuredRemote(ctx, "branch."+branch+".pushRemote")
	}
	if selected == "" {
		selected = b.configuredRemote(ctx, "remote.pushDefault")
	}
	if selected == "" && branchErr == nil {
		selected = b.configuredRemote(ctx, "branch."+strings.TrimSpace(branch)+".remote")
	}
	if selected == "" && remotes["origin"] {
		selected = "origin"
	}
	if selected == "" && len(remotes) == 1 {
		for remote := range remotes {
			selected = remote
		}
	}
	if !remotes[selected] {
		return ""
	}
	pushURLs, err := b.runImportGit(ctx, "remote", "get-url", "--all", "--push", selected)
	if err != nil || remoteURLListHasEmbeddedCredentials(pushURLs) {
		return ""
	}
	pushURL, single := singleRemoteURL(pushURLs)
	if !single || !isSSHRemoteURL(pushURL) {
		return ""
	}
	return pushURL
}

func (b *GitBackend) configuredRemote(ctx context.Context, key string) string {
	output, err := b.runImportGit(ctx, "config", "--includes", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

func (b *GitBackend) validateEffectiveRemote(ctx context.Context, remote, repositoryIdentity string) error {
	return b.validateEffectiveRemoteAt(ctx, "", remote, repositoryIdentity)
}

func (b *GitBackend) validateEffectiveRemoteAt(ctx context.Context, worktreePath, remote, repositoryIdentity string) error {
	expectedURL, hasExpectedURL := b.expectedRemoteURL(remote)
	for _, tc := range []struct {
		label string
		args  []string
	}{
		{label: "fetch", args: []string{"remote", "get-url", "--all", remote}},
		{label: "push", args: []string{"remote", "get-url", "--all", "--push", remote}},
	} {
		output, err := b.runImportGitAt(ctx, worktreePath, tc.args...)
		if err != nil {
			return NewError(CodeWorkspaceCreation,
				"failed to validate the new pull-request Git remote", false, err)
		}
		if remoteURLListHasEmbeddedCredentials(output) {
			return embeddedRemoteCredentialsError()
		}
		matches := singleRemoteURLMatches(output, repositoryIdentity)
		if hasExpectedURL {
			matches = singleRemoteURLEquals(output, expectedURL)
		}
		if !matches {
			return NewError(CodeWorkspaceCreation,
				fmt.Sprintf("new pull-request Git remote has an unsafe effective %s destination", tc.label),
				false, nil)
		}
	}
	return nil
}

func (b *GitBackend) rememberExpectedRemoteURL(remote, expectedURL string) {
	b.expectedRemoteURLsMu.Lock()
	defer b.expectedRemoteURLsMu.Unlock()
	b.expectedRemoteURLs[remote] = expectedURL
}

func (b *GitBackend) forgetExpectedRemoteURL(remote string) {
	b.expectedRemoteURLsMu.Lock()
	defer b.expectedRemoteURLsMu.Unlock()
	delete(b.expectedRemoteURLs, remote)
}

func (b *GitBackend) expectedRemoteURL(remote string) (string, bool) {
	b.expectedRemoteURLsMu.RLock()
	defer b.expectedRemoteURLsMu.RUnlock()
	expectedURL, ok := b.expectedRemoteURLs[remote]
	return expectedURL, ok
}

func forkSSHURL(projectURL string, repository Repository) (string, error) {
	projectURL = strings.TrimSpace(projectURL)
	repositoryPath := strings.Trim(repository.Owner, "/") + "/" + strings.Trim(repository.Name, "/") + ".git"
	if strings.Contains(projectURL, "://") {
		parsed, err := neturl.Parse(projectURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", fmt.Errorf("invalid project SSH URL")
		}
		parsed.Path = "/" + repositoryPath
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String(), nil
	}
	colon := strings.IndexByte(projectURL, ':')
	slash := strings.IndexAny(projectURL, `/\`)
	if colon < 0 || slash >= 0 && colon > slash {
		return "", fmt.Errorf("invalid project SSH URL")
	}
	return projectURL[:colon+1] + repositoryPath, nil
}

func singleRemoteURLEquals(output, expected string) bool {
	remoteURL, single := singleRemoteURL(output)
	return single && remoteURLsEqual(remoteURL, expected)
}

func remoteURLsEqual(left, right string) bool {
	return strings.TrimSpace(left) == strings.TrimSpace(right)
}

func isSSHRemoteURL(remoteURL string) bool {
	remoteURL = strings.TrimSpace(remoteURL)
	lower := strings.ToLower(remoteURL)
	if strings.HasPrefix(lower, "ssh://") || strings.HasPrefix(lower, "git+ssh://") {
		return true
	}
	if strings.Contains(lower, "://") {
		return false
	}
	colon := strings.IndexByte(remoteURL, ':')
	slash := strings.IndexAny(remoteURL, `/\`)
	return colon >= 0 && (slash < 0 || colon < slash)
}

func singleRemoteURLMatches(output, repositoryIdentity string) bool {
	remoteURL, single := singleRemoteURL(output)
	if !single {
		return false
	}
	identity, ok := urlutil.CanonicalRepositoryIdentityFromRemote(remoteURL)
	return ok && EqualRepositoryIdentity(identity, repositoryIdentity)
}

func singleRemoteURL(output string) (string, bool) {
	remoteURL := ""
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if remoteURL != "" {
			return "", false
		}
		remoteURL = line
	}
	return remoteURL, remoteURL != ""
}

func (b *GitBackend) remoteHasCustomPushRefspec(ctx context.Context, remote string) bool {
	output, err := b.runImportGit(ctx, "config", "--includes", "--null", "--list")
	if err != nil {
		return true
	}
	want := "remote." + remote + ".push"
	for record := range strings.SplitSeq(output, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		if strings.EqualFold(key, want) {
			return true
		}
	}
	return false
}

func (b *GitBackend) Fetch(ctx context.Context, remote, sourceRef, destinationRef string) (string, error) {
	refspec := "+" + sourceRef + ":" + destinationRef
	if _, err := b.git.RunWithEnvironmentAndDisabledHooks(ctx, b.setupEnvironment, "fetch", "--no-tags", remote, refspec); err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case isGitAuthenticationFailure(message):
			return "", NewError(CodeAuthentication, "Git authentication failed while fetching the pull-request head", false, err)
		case isGitNetworkFailure(message):
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

func isGitAuthenticationFailure(message string) bool {
	patterns := []string{
		"authentication failed", "permission denied", "could not read username", "could not read password",
		"terminal prompts disabled", "access denied", "returned error: 401", "returned error: 403",
		"http 401", "http 403",
	}
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func isGitNetworkFailure(message string) bool {
	patterns := []string{
		"could not resolve", "unable to access", "connection timed out", "connection refused",
		"failed to connect", "network is unreachable", "connection reset",
	}
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func (b *GitBackend) Create(ctx context.Context, branch, baseRef string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	var ownershipCleanup func(context.Context) (string, string, error)
	path, createErr := b.manager.AddFromBaseWithOptions(branch, baseRef, "", worktree.AddOptions{
		Context: ctx, SkipSetup: true, SetupEnvironment: b.setupEnvironment,
		BeforeCheckout: b.validateWorktreeConfiguration,
		CaptureCleanup: func(cleanup func(context.Context) (string, string, error)) {
			ownershipCleanup = cleanup
		},
	})
	workspaceBranch := branch
	partialCreation := false
	if createErr != nil {
		var partial interface {
			PartialWorktree() (string, string)
		}
		if errors.As(createErr, &partial) {
			partialPath, partialBranch := partial.PartialWorktree()
			if partialPath != "" || partialBranch != "" {
				path = partialPath
				workspaceBranch = partialBranch
				partialCreation = true
			}
		}
	}
	if createErr != nil && path == "" && !partialCreation {
		message := strings.ToLower(createErr.Error())
		if strings.Contains(message, "already exists") || strings.Contains(message, "already checked out") ||
			strings.Contains(message, "not an empty directory") || strings.Contains(message, "already registered worktree") {
			return Workspace{}, NewError(CodeNamingConflict,
				"the generated pull-request branch or workspace path is already in use", false, createErr)
		}
		return Workspace{}, createErr
	}
	info, ok := urlutil.CanonicalRepositoryInfo(b.project.Identity)
	if !ok {
		return Workspace{}, fmt.Errorf("invalid project repository identity %q", b.project.Identity)
	}
	workspace := Workspace{
		ID:         b.project.Identity + ":" + workspaceBranch + ":" + template.ShortHash(path),
		Repository: b.project.Identity, Branch: workspaceBranch, Path: path, State: "ready",
		SessionName: tmux.WorkspaceSessionName(info, workspaceBranch, path),
	}
	if ownershipCleanup != nil {
		workspace.partialCleanup = &workspacePartialCleanup{run: func(cleanupCtx context.Context) error {
			remainingPath, remainingBranch, cleanupErr := ownershipCleanup(cleanupCtx)
			if cleanupErr != nil {
				return cleanupErr
			}
			if remainingPath != "" || remainingBranch != "" {
				return fmt.Errorf("ownership-safe cleanup preserved path %q and branch %q", remainingPath, remainingBranch)
			}
			return nil
		}}
	}
	var concretePartial *gitadapter.PartialWorktreeCreationError
	if errors.As(createErr, &concretePartial) {
		workspace.partialCleanup = &workspacePartialCleanup{
			run: func(cleanupCtx context.Context) error {
				return concretePartial.RetryCleanup(cleanupCtx, b.git, b.setupEnvironment)
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

func (b *GitBackend) ConfigurePush(ctx context.Context, workspace Workspace, remote, sourceRepository, sourceBranch string) error {
	if err := b.validateWorkspaceHead(ctx, workspace); err != nil {
		return err
	}
	if err := b.validateEffectiveRemoteAt(ctx, workspace.Path, remote, sourceRepository); err != nil {
		return err
	}
	if _, err := b.runImportGit(ctx, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	hooksPath, err := b.configureDisabledHooks(ctx, workspace.Path)
	if err != nil {
		return err
	}
	commands := [][]string{
		{"-C", workspace.Path, "config", "--worktree", "branch." + workspace.Branch + ".remote", remote},
		{"-C", workspace.Path, "config", "--worktree", "branch." + workspace.Branch + ".pushRemote", remote},
		{"-C", workspace.Path, "config", "--worktree", "branch." + workspace.Branch + ".merge", "refs/heads/" + sourceBranch},
		{"-C", workspace.Path, "config", "--worktree", "remote." + remote + ".push", "HEAD:refs/heads/" + sourceBranch},
		{"-C", workspace.Path, "config", "--worktree", "remote." + remote + ".mirror", "false"},
		{"-C", workspace.Path, "config", "--worktree", "push.default", "upstream"},
		{"-C", workspace.Path, "config", "--worktree", "push.followTags", "false"},
	}
	for _, args := range commands {
		if _, err := b.git.RunWithContext(ctx, args...); err != nil {
			return err
		}
	}
	return b.validateWorkspacePushRouting(ctx, workspace, remote, sourceRepository, sourceBranch, hooksPath)
}

func (b *GitBackend) configureDisabledHooks(ctx context.Context, worktreePath string) (string, error) {
	output, err := b.runImportGitAt(ctx, worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to locate Git metadata for hook isolation", false, err)
	}
	commonDir := strings.TrimSpace(output)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to resolve Git metadata for hook isolation", false, err)
	}
	hooksPath := filepath.Join(filepath.Clean(commonDir), "kwt", "disabled-hooks")
	for _, directory := range []string{filepath.Dir(hooksPath), hooksPath} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return "", NewError(CodeWorkspaceCreation, "failed to prepare persistent hook isolation", false, err)
		}
	}
	entries, err := os.ReadDir(hooksPath)
	if err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to inspect persistent hook isolation", false, err)
	}
	if len(entries) != 0 {
		return "", NewError(CodeWorkspaceCreation, "persistent hook isolation directory is not empty", false, nil)
	}
	if _, err := b.runImportGitAt(ctx, worktreePath,
		"config", "--worktree", "core.hooksPath", hooksPath); err != nil {
		return "", NewError(CodeWorkspaceCreation, "failed to persist hook isolation", false, err)
	}
	return hooksPath, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a trusted directory", path)
	}
	return os.Chmod(path, 0o700)
}

func (b *GitBackend) validateWorkspaceHead(ctx context.Context, workspace Workspace) error {
	branch, err := b.runImportGitAt(ctx, workspace.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != workspace.Branch {
		return NewError(CodeWorkspaceCreation,
			fmt.Sprintf("pull-request worktree is no longer on generated branch %q", workspace.Branch),
			false, err)
	}
	return nil
}

func (b *GitBackend) validateWorkspacePushRouting(
	ctx context.Context,
	workspace Workspace,
	remote, sourceRepository, sourceBranch, expectedHooksPath string,
) error {
	if err := b.validateImportConfigurationAt(ctx, workspace.Path); err != nil {
		return err
	}
	if err := b.validateWorkspaceHead(ctx, workspace); err != nil {
		return err
	}
	if err := b.validateEffectiveRemoteAt(ctx, workspace.Path, remote, sourceRepository); err != nil {
		return err
	}
	hooksOutput, err := b.git.RunWithContext(ctx,
		"-C", workspace.Path, "config", "--includes", "--path", "--get", "core.hooksPath")
	if err != nil || filepath.Clean(strings.TrimSpace(hooksOutput)) != filepath.Clean(expectedHooksPath) {
		return NewError(CodeWorkspaceCreation,
			"pull-request worktree does not retain persistent hook isolation", false, err)
	}

	pushKey := "remote." + remote + ".push"
	pushOutput, err := b.runImportGitAt(ctx, workspace.Path, "config", "--includes", "--get-all", pushKey)
	if err != nil {
		return NewError(CodeWorkspaceCreation, "failed to validate pull-request push configuration", false, err)
	}
	pushValue, single := singleRemoteURL(pushOutput)
	if !single || pushValue != "HEAD:refs/heads/"+sourceBranch {
		return NewError(CodeWorkspaceCreation,
			fmt.Sprintf("pull-request worktree has unsafe push configuration for %s", pushKey), false, nil)
	}

	expectedValues := map[string]string{
		"branch." + workspace.Branch + ".remote":     remote,
		"branch." + workspace.Branch + ".pushRemote": remote,
		"branch." + workspace.Branch + ".merge":      "refs/heads/" + sourceBranch,
		"remote." + remote + ".mirror":               "false",
		"push.default":                               "upstream",
		"push.followTags":                            "false",
	}
	for key, expected := range expectedValues {
		output, err := b.runImportGitAt(ctx, workspace.Path, "config", "--includes", "--get", key)
		if err != nil {
			return NewError(CodeWorkspaceCreation, "failed to validate pull-request push configuration", false, err)
		}
		if strings.TrimSpace(output) != expected {
			return NewError(CodeWorkspaceCreation,
				fmt.Sprintf("pull-request worktree has unsafe push configuration for %s", key), false, nil)
		}
	}
	return nil
}

func (b *GitBackend) Rollback(ctx context.Context, workspace Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if workspace.partialCleanup != nil {
		return workspace.partialCleanup.run(ctx)
	}
	return b.manager.RemoveWithBranchWithEnvironment(workspace.Path, workspace.Branch, true, true, true, b.setupEnvironment)
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
