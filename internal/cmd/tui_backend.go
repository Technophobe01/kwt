package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.kenn.io/kwt/internal/config"
	"go.kenn.io/kwt/internal/discovery"
	"go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/internal/registry"
	"go.kenn.io/kwt/internal/status"
	"go.kenn.io/kwt/internal/tmux"
	dashboard "go.kenn.io/kwt/internal/tui"
	"go.kenn.io/kwt/internal/url"
	"go.kenn.io/kwt/internal/worktree"
	"go.kenn.io/kwt/pkg/models"
)

type tuiBackend struct {
	cfg                      *models.Config
	tmux                     *tmux.TmuxCommand
	launchDir                string
	discoverGlobalWorktrees  func(string) ([]*discovery.GlobalWorktreeEntry, error)
	discoverProjectWorktrees func(string) ([]*discovery.GlobalWorktreeEntry, error)
	discoverLaunchWorktrees  func(string) ([]*discovery.GlobalWorktreeEntry, error)
	collectStatuses          func(context.Context, string, []*discovery.GlobalWorktreeEntry) (map[string]*models.WorktreeStatus, error)
	listSessions             func() ([]string, error)
	registerProject          func(models.Project) error
}

func newTUIBackend(cfg *models.Config) *tuiBackend {
	launchDir, _ := os.Getwd()
	return newTUIBackendWithLaunchDir(cfg, launchDir)
}

func newTUIBackendWithLaunchDir(cfg *models.Config, launchDir string) *tuiBackend {
	tmuxCmd := tmux.NewTmuxCommand("")
	return &tuiBackend{
		cfg:                      cfg,
		tmux:                     tmuxCmd,
		launchDir:                launchDir,
		discoverGlobalWorktrees:  discovery.DiscoverGlobalWorktrees,
		discoverProjectWorktrees: discoverLaunchRepoWorktrees,
		discoverLaunchWorktrees:  discoverLaunchRepoWorktrees,
		collectStatuses:          collectTUIStatuses,
		listSessions:             tmuxCmd.ListSessions,
		registerProject:          config.RegisterProject,
	}
}

func (b *tuiBackend) List(ctx context.Context) ([]dashboard.Row, error) {
	entries, err := b.discoverGlobalWorktrees(b.cfg.Worktree.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to discover worktrees: %w", err)
	}

	entries = mergeTUIEntries(entries, b.discoverRegisteredProjectWorktrees())

	launchEntries, err := b.discoverLaunchWorktrees(b.launchDir)
	if err != nil {
		return nil, fmt.Errorf("failed to discover launch repository worktrees: %w", err)
	}
	b.registerLaunchProject(launchEntries)
	entries = mergeTUIEntries(entries, launchEntries)

	statusByPath, err := b.collectStatuses(ctx, b.cfg.Worktree.BaseDir, entries)
	if err != nil {
		return nil, err
	}

	sessions, err := b.listSessions()
	if err != nil {
		return nil, err
	}
	liveSessions := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		liveSessions[session] = true
	}

	rows := make([]dashboard.Row, 0, len(entries))
	for _, entry := range entries {
		st := statusByPath[entry.Path]
		if st == nil {
			st = unknownStatusForEntry(entry)
		}
		rows = append(rows, buildTUIRow(entry, st, liveSessions))
	}
	return rows, nil
}

func (b *tuiBackend) discoverRegisteredProjectWorktrees() []*discovery.GlobalWorktreeEntry {
	var entries []*discovery.GlobalWorktreeEntry
	for _, project := range b.cfg.Projects {
		if project.Path == "" {
			continue
		}
		projectEntries, err := b.discoverProjectWorktrees(project.Path)
		if err != nil {
			continue
		}
		projectEntries = applyProjectIdentityFallback(projectEntries, project)
		entries = mergeTUIEntries(entries, projectEntries)
	}
	return entries
}

func (b *tuiBackend) registerLaunchProject(entries []*discovery.GlobalWorktreeEntry) {
	if b.registerProject == nil {
		return
	}
	project, ok := projectFromEntries(entries, b.launchDir)
	if !ok {
		return
	}
	if existing, found := b.projectByPath(project.Path); found && shouldReuseExistingProject(existing, project) {
		project = existing
	}
	if err := b.registerProject(project); err != nil {
		return
	}
	b.upsertProject(project)
}

func (b *tuiBackend) projectByPath(projectPath string) (models.Project, bool) {
	if b.cfg == nil || projectPath == "" {
		return models.Project{}, false
	}
	for _, project := range b.cfg.Projects {
		if project.Path != "" && samePath(project.Path, projectPath) {
			return project, true
		}
	}
	return models.Project{}, false
}

func projectFromEntries(entries []*discovery.GlobalWorktreeEntry, fallbackPath string) (models.Project, bool) {
	var info *url.RepositoryInfo
	projectPath := fallbackPath
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if info == nil && entry.RepositoryInfo != nil {
			info = entry.RepositoryInfo
		}
		if entry.IsMain && entry.Path != "" {
			projectPath = entry.Path
		}
	}
	if info == nil || projectPath == "" {
		return models.Project{}, false
	}
	repository := info.FullPath
	if repository == "" {
		repository = path.Join(info.Host, info.Owner, info.Repository)
	}
	return models.Project{
		Repository: repository,
		Name:       info.Repository,
		Path:       projectPath,
	}, true
}

func (b *tuiBackend) upsertProject(project models.Project) {
	if b.cfg == nil {
		return
	}
	for i := range b.cfg.Projects {
		if sameRegisteredProject(b.cfg.Projects[i], project) {
			b.cfg.Projects[i] = project
			return
		}
	}
	b.cfg.Projects = append(b.cfg.Projects, project)
}

func sameRegisteredProject(a, b models.Project) bool {
	// A project row represents one local checkout. Matching paths should share
	// a registry slot so path-fallback identities can be upgraded to remotes.
	if a.Path != "" && b.Path != "" && samePath(a.Path, b.Path) {
		return true
	}
	if a.Repository != "" && b.Repository != "" {
		return strings.EqualFold(a.Repository, b.Repository)
	}
	return false
}

func shouldReuseExistingProject(existing, discovered models.Project) bool {
	return hasStableProjectIdentity(existing) && !hasStableProjectIdentity(discovered)
}

func hasStableProjectIdentity(project models.Project) bool {
	repository := strings.TrimSpace(project.Repository)
	return repository != "" && !isAbsoluteSlashPath(repository)
}

func discoverLaunchRepoWorktrees(launchDir string) ([]*discovery.GlobalWorktreeEntry, error) {
	if launchDir == "" {
		return nil, nil
	}

	g := git.New(launchDir)
	worktrees, err := g.ListWorktrees()
	if err != nil {
		return nil, nil
	}

	rootPath := launchDir
	if repoRoot, err := g.GetMainRepositoryPath(); err == nil {
		rootPath = repoRoot
	}
	repoURL := ""
	repoInfo := repositoryInfoFromRootPath(rootPath)
	if gotURL, err := g.GetRepositoryURL(); err == nil {
		if parsedInfo, parseErr := url.ParseRepositoryURL(gotURL); parseErr == nil {
			repoURL = gotURL
			repoInfo = parsedInfo
		}
	}

	entries := make([]*discovery.GlobalWorktreeEntry, 0, len(worktrees))
	for _, wt := range worktrees {
		entries = append(entries, &discovery.GlobalWorktreeEntry{
			RepositoryURL:  repoURL,
			RepositoryInfo: repoInfo,
			Branch:         wt.Branch,
			Path:           wt.Path,
			CommitHash:     wt.CommitHash,
			IsMain:         wt.IsMain,
		})
	}
	return entries, nil
}

func applyProjectIdentityFallback(
	entries []*discovery.GlobalWorktreeEntry,
	project models.Project,
) []*discovery.GlobalWorktreeEntry {
	fallback := repositoryInfoFromProject(project)
	if fallback == nil {
		return entries
	}
	for _, entry := range entries {
		if entry == nil || entry.RepositoryURL != "" {
			continue
		}
		entry.RepositoryInfo = fallback
	}
	return entries
}

func repositoryInfoFromProject(project models.Project) *url.RepositoryInfo {
	repository := strings.TrimSpace(project.Repository)
	if repository != "" {
		if info, err := url.ParseRepositoryURL(repository); err == nil {
			return info
		}
	}

	name := strings.TrimSpace(project.Name)
	if name == "" && project.Path != "" {
		name = filepath.Base(project.Path)
	}
	fullPath := repository
	if fullPath == "" {
		fullPath = project.Path
	}
	if name == "" || fullPath == "" {
		return nil
	}
	return &url.RepositoryInfo{
		Repository: name,
		FullPath:   filepath.ToSlash(fullPath),
	}
}

func repositoryInfoFromRootPath(rootPath string) *url.RepositoryInfo {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil
	}
	cleanPath := rootPath
	if absPath, err := filepath.Abs(cleanPath); err == nil {
		cleanPath = absPath
	}
	if resolvedPath, err := filepath.EvalSymlinks(cleanPath); err == nil {
		cleanPath = resolvedPath
	}
	name := filepath.Base(cleanPath)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = filepath.ToSlash(cleanPath)
	}
	return &url.RepositoryInfo{
		Repository: name,
		FullPath:   filepath.ToSlash(cleanPath),
	}
}

func mergeTUIEntries(
	globalEntries []*discovery.GlobalWorktreeEntry,
	launchEntries []*discovery.GlobalWorktreeEntry,
) []*discovery.GlobalWorktreeEntry {
	entries := append([]*discovery.GlobalWorktreeEntry(nil), globalEntries...)
	seen := make(map[string]int, len(entries))
	for i, entry := range entries {
		if entry != nil && entry.Path != "" {
			seen[entryPathKey(entry.Path)] = i
		}
	}
	for _, entry := range launchEntries {
		if entry == nil || entry.Path == "" {
			continue
		}
		key := entryPathKey(entry.Path)
		if existing, ok := seen[key]; ok {
			if shouldReplaceTUIEntry(entries[existing], entry) {
				entries[existing] = entry
			}
			continue
		}
		entries = append(entries, entry)
		seen[key] = len(entries) - 1
	}
	return entries
}

func entryPathKey(entryPath string) string {
	return cleanComparablePath(entryPath)
}

func shouldReplaceTUIEntry(
	existing *discovery.GlobalWorktreeEntry,
	incoming *discovery.GlobalWorktreeEntry,
) bool {
	if existing == nil {
		return incoming != nil
	}
	if incoming == nil {
		return false
	}
	if incoming.RepositoryURL != "" && existing.RepositoryURL == "" {
		return true
	}
	return usesPathFallbackIdentity(existing) && hasStableNonPathIdentity(incoming)
}

func usesPathFallbackIdentity(entry *discovery.GlobalWorktreeEntry) bool {
	if entry == nil || entry.RepositoryURL != "" || entry.RepositoryInfo == nil {
		return false
	}
	return isAbsoluteSlashPath(entry.RepositoryInfo.FullPath)
}

func hasStableNonPathIdentity(entry *discovery.GlobalWorktreeEntry) bool {
	if entry == nil || entry.RepositoryInfo == nil {
		return false
	}
	info := entry.RepositoryInfo
	if info.FullPath != "" && !isAbsoluteSlashPath(info.FullPath) {
		return true
	}
	return info.Host != "" && info.Repository != ""
}

func isAbsoluteSlashPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if filepath.IsAbs(filepath.FromSlash(value)) {
		return true
	}
	slashPath := strings.ReplaceAll(value, `\`, "/")
	// Treat leading-slash paths as absolute fallbacks even when running on
	// Windows, where filepath.IsAbs("\\path") is false without a drive.
	return strings.HasPrefix(slashPath, "/") || isWindowsDriveAbsolutePath(slashPath)
}

func isWindowsDriveAbsolutePath(value string) bool {
	if len(value) < 3 || value[1] != ':' || value[2] != '/' {
		return false
	}
	drive := value[0]
	return (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')
}

func collectTUIStatuses(
	ctx context.Context,
	baseDir string,
	entries []*discovery.GlobalWorktreeEntry,
) (map[string]*models.WorktreeStatus, error) {
	worktrees := make([]*models.Worktree, 0, len(entries))
	for _, entry := range entries {
		worktrees = append(worktrees, &models.Worktree{
			Path:       entry.Path,
			Branch:     entry.Branch,
			CommitHash: entry.CommitHash,
			IsMain:     entry.IsMain,
		})
	}

	collector := status.NewStatusCollectorWithOptions(tuiStatusCollectorOptions(baseDir))
	statuses, err := collector.CollectAll(ctx, worktrees)
	if err != nil {
		return nil, err
	}
	statusByPath := make(map[string]*models.WorktreeStatus, len(statuses))
	for _, st := range statuses {
		statusByPath[st.Path] = st
	}
	return statusByPath, nil
}

func tuiStatusCollectorOptions(baseDir string) status.StatusCollectorOptions {
	return status.StatusCollectorOptions{
		FetchRemote: true,
		BaseDir:     baseDir,
	}
}

func buildTUIRow(
	entry *discovery.GlobalWorktreeEntry,
	st *models.WorktreeStatus,
	liveSessions map[string]bool,
) dashboard.Row {
	sessionName := ""
	sessionLive := false
	if entry.RepositoryInfo != nil {
		sessionName = tmux.WorkspaceSessionName(entry.RepositoryInfo, entry.Branch, entry.Path)
		sessionLive = liveSessions[sessionName]
	}
	return dashboard.Row{
		Entry:       entry,
		Status:      st,
		SessionName: sessionName,
		SessionLive: sessionLive,
	}
}

func (b *tuiBackend) CreateWorktree(ctx context.Context, row dashboard.Row, branch string) (string, error) {
	if row.Entry == nil {
		return "", fmt.Errorf("no worktree selected")
	}
	return worktree.New(git.New(row.Entry.Path), b.cfg).Add(branch, "", true)
}

func (b *tuiBackend) RemoveWorktree(ctx context.Context, row dashboard.Row) error {
	if row.Entry == nil {
		return fmt.Errorf("no worktree selected")
	}
	if row.Entry.IsMain {
		return fmt.Errorf("refusing to remove a main worktree")
	}

	repoRoot, err := b.repositoryRootForRow(row)
	if err != nil {
		return err
	}
	if err := b.removeWorktreeFromRoot(repoRoot, row.Entry.Path); err != nil {
		if strings.Contains(err.Error(), "contains modified or untracked files") ||
			strings.Contains(err.Error(), "has local changes") {
			return fmt.Errorf("worktree has uncommitted changes (use kwt remove --force)")
		}
		return err
	}

	if reg, err := registry.New(); err == nil {
		_ = reg.Unregister(row.Entry.Path)
	}

	if row.SessionLive && row.SessionName != "" {
		return b.tmux.KillSession(row.SessionName)
	}
	return nil
}

func (b *tuiBackend) repositoryRootForRow(row dashboard.Row) (string, error) {
	if row.Entry == nil {
		return "", fmt.Errorf("no worktree selected")
	}

	repoRoot, err := git.New(row.Entry.Path).GetMainRepositoryPath()
	if err == nil {
		return repoRoot, nil
	}
	directErr := err

	if b.cfg != nil {
		for _, project := range b.cfg.Projects {
			if !projectMatchesRow(project, row) {
				continue
			}
			repoRoot, err := git.New(project.Path).GetMainRepositoryPath()
			if err == nil {
				return repoRoot, nil
			}
		}
	}

	return "", fmt.Errorf("failed to find repository root: %w", directErr)
}

func projectMatchesRow(project models.Project, row dashboard.Row) bool {
	if project.Path == "" || row.Entry == nil || row.Entry.RepositoryInfo == nil {
		return false
	}

	info := row.Entry.RepositoryInfo
	stableCandidates := rowRepositoryIdentityCandidates(info)
	for _, candidate := range stableCandidates {
		if candidate != "" && strings.EqualFold(project.Repository, candidate) {
			return true
		}
	}
	if len(stableCandidates) > 0 {
		return false
	}
	if project.Repository != "" && info.Repository != "" && strings.EqualFold(project.Repository, info.Repository) {
		return true
	}
	return project.Name != "" && strings.EqualFold(project.Name, info.Repository)
}

func rowRepositoryIdentityCandidates(info *url.RepositoryInfo) []string {
	if info == nil {
		return nil
	}
	var candidates []string
	if info.FullPath != "" {
		candidates = append(candidates, info.FullPath)
	}
	if info.Host != "" && info.Owner != "" && info.Repository != "" {
		candidates = append(candidates, path.Join(info.Host, info.Owner, info.Repository))
	}
	return candidates
}

func (b *tuiBackend) removeWorktreeFromRoot(repoRoot string, worktreePath string) error {
	err := worktree.New(git.New(repoRoot), b.cfg).Remove(worktreePath, false)
	if err == nil || !isWorktreeValidationError(err) {
		return err
	}

	if repairErr := repairLinkedWorktreeGitFile(repoRoot, worktreePath); repairErr != nil {
		return fmt.Errorf("%w (failed to repair worktree metadata: %v)", err, repairErr)
	}
	return worktree.New(git.New(repoRoot), b.cfg).Remove(worktreePath, false)
}

func isWorktreeValidationError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "validation failed, cannot remove working tree") &&
		strings.Contains(text, ".git")
}

func repairLinkedWorktreeGitFile(repoRoot string, worktreePath string) error {
	gitFilePath := filepath.Join(worktreePath, ".git")
	info, err := os.Lstat(gitFilePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", gitFilePath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", gitFilePath)
	}

	adminDir, err := findLinkedWorktreeAdminDir(repoRoot, worktreePath)
	if err != nil {
		return err
	}

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0644
	}
	return writeReplacementFile(gitFilePath, []byte("gitdir: "+adminDir+"\n"), mode)
}

func writeReplacementFile(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".git.repair-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	n, err := tmp.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func findLinkedWorktreeAdminDir(repoRoot string, worktreePath string) (string, error) {
	commonDir, err := git.New(repoRoot).RunCommand("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoRoot, commonDir)
	}

	worktreesDir := filepath.Join(commonDir, "worktrees")
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return "", err
	}

	wantGitFile := filepath.Join(worktreePath, ".git")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		adminDir := filepath.Join(worktreesDir, entry.Name())
		gitdirPath := filepath.Join(adminDir, "gitdir")
		data, err := os.ReadFile(gitdirPath)
		if err != nil {
			continue
		}
		gotGitFile := strings.TrimSpace(string(data))
		if !filepath.IsAbs(gotGitFile) {
			gotGitFile = filepath.Join(adminDir, gotGitFile)
		}
		if samePath(gotGitFile, wantGitFile) {
			return adminDir, nil
		}
	}

	return "", fmt.Errorf("no worktree admin dir found for %s", worktreePath)
}

func samePath(a string, b string) bool {
	a = cleanComparablePath(a)
	b = cleanComparablePath(b)
	return a == b
}

func cleanComparablePath(path string) string {
	cleaned := filepath.Clean(path)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	return cleaned
}

func (b *tuiBackend) KillSession(row dashboard.Row) error {
	if row.SessionName == "" {
		return fmt.Errorf("no live workspace")
	}
	return b.tmux.KillSession(row.SessionName)
}

func (b *tuiBackend) OpenInTmux(ctx context.Context, row dashboard.Row, layoutName string) error {
	return b.attachWorkspace(ctx, row, layoutName, true, false)
}

func (b *tuiBackend) AttachOutsideTmux(row dashboard.Row, layoutName string) error {
	return b.attachWorkspace(context.Background(), row, layoutName, false, config.StdinInteractive())
}

func (b *tuiBackend) LayoutNames() []string {
	names := make([]string, 0, len(b.cfg.Layouts.Presets))
	for _, layout := range b.cfg.Layouts.Presets {
		names = append(names, layout.Name)
	}
	return names
}

func (b *tuiBackend) InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

func (b *tuiBackend) attachWorkspace(ctx context.Context, row dashboard.Row, layoutName string, insideTmux bool, interactive bool) error {
	if row.Entry == nil {
		return fmt.Errorf("no worktree selected")
	}
	sessionName, err := b.sessionName(row)
	if err != nil {
		return err
	}
	layout, err := b.resolveLayout(row, layoutName, interactive)
	if err != nil {
		return err
	}
	return tmux.NewWorkspaceRunner(b.tmux).EnsureAndAttach(ctx, sessionName, row.Entry.Path, layout, insideTmux)
}

func (b *tuiBackend) resolveLayout(row dashboard.Row, layoutName string, interactive bool) (models.Layout, error) {
	var layout models.Layout
	var err error
	if layoutName != "" {
		layout, err = tmux.ResolveLayout(b.cfg.Layouts, layoutName, false, "", nil)
	} else {
		var repoRoot string
		repoRoot, err = b.repositoryRootForRow(row)
		if err != nil {
			return models.Layout{}, err
		}
		var targetDefault string
		targetDefault, err = config.LoadRepoLayoutDefault(repoRoot, interactive)
		if err != nil {
			return models.Layout{}, err
		}
		layout, err = tmux.ResolveLayout(b.cfg.Layouts, "", false, targetDefault, nil)
	}
	if err != nil {
		return models.Layout{}, err
	}
	return tmux.ResolvePaneCommands(layout, b.cfg.Agents)
}

func (b *tuiBackend) sessionName(row dashboard.Row) (string, error) {
	if row.SessionName != "" {
		return row.SessionName, nil
	}
	if row.Entry == nil || row.Entry.RepositoryInfo == nil {
		return "", fmt.Errorf("could not resolve repository info for %s", rowPathForHandoff(row))
	}
	return tmux.WorkspaceSessionName(row.Entry.RepositoryInfo, row.Entry.Branch, row.Entry.Path), nil
}

func unknownStatusForEntry(entry *discovery.GlobalWorktreeEntry) *models.WorktreeStatus {
	repo := ""
	if entry.RepositoryInfo != nil {
		repo = entry.RepositoryInfo.Repository
	}
	return &models.WorktreeStatus{
		Path:       entry.Path,
		Branch:     entry.Branch,
		Repository: repo,
		Status:     models.WorktreeStatusUnknown,
	}
}
