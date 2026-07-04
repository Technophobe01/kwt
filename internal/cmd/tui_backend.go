package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.kenn.io/kwt/internal/config"
	"go.kenn.io/kwt/internal/discovery"
	"go.kenn.io/kwt/internal/fleet"
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
	readFleetState           func(context.Context, *models.Config) (fleet.FleetState, error)
	now                      func() time.Time
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
		readFleetState:           readTUIFleetState,
		now:                      time.Now,
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
	rows = b.mergeFleetRows(ctx, rows)
	return rows, nil
}

func (b *tuiBackend) mergeFleetRows(ctx context.Context, rows []dashboard.Row) []dashboard.Row {
	if b.cfg == nil || !b.cfg.Fleet.Enabled || b.readFleetState == nil {
		return rows
	}
	state, err := b.readFleetState(ctx, b.cfg)
	if err != nil {
		return rows
	}
	currentHost, err := currentFleetHostID(b.cfg)
	if err != nil {
		return rows
	}

	byKey := make(map[string]int, len(rows))
	for i := range rows {
		if key := tuiFleetKeyForRow(rows[i]); key != "" {
			byKey[key] = i
		}
	}

	rendered := fleet.BuildStatusRows(state, currentHost, b.now())
	for i, fleetRow := range state.Rows {
		info := dashboardFleetInfo(fleetRow, renderedStatusRow(rendered, i), currentHost)
		if info == nil {
			continue
		}
		key := tuiFleetKey(info.ProjectIdentity, info.Kind, info.Ref)
		if rowIndex, ok := byKey[key]; ok {
			rows[rowIndex].Fleet = info
			continue
		}
		if info.Local {
			continue
		}
		rows = append(rows, dashboard.Row{Fleet: info})
	}
	return rows
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

func readTUIFleetState(ctx context.Context, cfg *models.Config) (fleet.FleetState, error) {
	client, err := newFleetClientFromConfig(cfg)
	if err != nil {
		return fleet.FleetState{}, err
	}
	state, _, notModified, err := client.State(ctx, "")
	if err != nil {
		return fleet.FleetState{}, err
	}
	if notModified {
		return fleet.FleetState{}, nil
	}
	return state, nil
}

func tuiFleetKeyForRow(row dashboard.Row) string {
	if row.Fleet != nil {
		return tuiFleetKey(row.Fleet.ProjectIdentity, row.Fleet.Kind, row.Fleet.Ref)
	}
	if row.Entry == nil || row.Entry.RepositoryInfo == nil {
		return ""
	}
	identity := row.Entry.RepositoryInfo.FullPath
	if identity == "" && row.Entry.RepositoryInfo.Host != "" && row.Entry.RepositoryInfo.Owner != "" && row.Entry.RepositoryInfo.Repository != "" {
		identity = path.Join(row.Entry.RepositoryInfo.Host, row.Entry.RepositoryInfo.Owner, row.Entry.RepositoryInfo.Repository)
	}
	if identity == "" || row.Entry.Branch == "" {
		return ""
	}
	return tuiFleetKey(identity, "branch", row.Entry.Branch)
}

func tuiFleetKey(projectIdentity string, kind string, ref string) string {
	projectIdentity = strings.ToLower(strings.TrimSpace(projectIdentity))
	kind = strings.ToLower(strings.TrimSpace(kind))
	ref = strings.TrimSpace(ref)
	if projectIdentity == "" || kind == "" || ref == "" {
		return ""
	}
	return projectIdentity + "\x00" + kind + "\x00" + ref
}

func renderedStatusRow(rows []fleet.StatusRow, index int) fleet.StatusRow {
	if index < 0 || index >= len(rows) {
		return fleet.StatusRow{}
	}
	return rows[index]
}

func dashboardFleetInfo(row fleet.FleetRow, rendered fleet.StatusRow, currentHost string) *dashboard.FleetInfo {
	ref := strings.TrimSpace(row.Ref)
	if ref == "" {
		ref = strings.TrimSpace(row.Branch)
	}
	if row.ProjectIdentity == "" || row.Kind == "" || ref == "" {
		return nil
	}
	info := &dashboard.FleetInfo{
		ProjectIdentity: row.ProjectIdentity,
		ProjectName:     rendered.Project,
		Kind:            row.Kind,
		Ref:             ref,
		Branch:          strings.TrimSpace(row.Branch),
		Local:           fleetRowHasHost(row.Observations, currentHost),
		Hosts:           fleetDisplayHosts(row.Observations, currentHost),
		Sync:            rendered.Sync,
		Dirty:           rendered.Dirty,
		Freshness:       rendered.Freshness,
	}
	if info.ProjectName == "" {
		info.ProjectName = strings.TrimSpace(row.ProjectName)
	}
	if info.ProjectName == "" {
		info.ProjectName = row.ProjectIdentity
	}
	if info.Branch == "" && row.Kind == "branch" {
		info.Branch = ref
	}
	info.MaterializeLabel = info.Branch
	if info.MaterializeLabel == "" {
		info.MaterializeLabel = info.Ref
	}
	if !info.Local {
		if observation, ok := fleetMaterializeObservation(row.Observations); ok {
			info.MaterializeHost = observation.HostID
			info.RemotePath = observation.Path
		}
		info.CanMaterialize = row.Kind == "branch" && info.Branch != ""
	}
	return info
}

func fleetRowHasHost(observations []fleet.Observation, hostID string) bool {
	hostID = strings.TrimSpace(hostID)
	for _, observation := range observations {
		if observation.HostID == hostID {
			return true
		}
	}
	return false
}

func fleetDisplayHosts(observations []fleet.Observation, currentHost string) []string {
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		hostID := strings.TrimSpace(observation.HostID)
		if hostID == "" {
			continue
		}
		if hostID == currentHost {
			hostID = "local"
		}
		seen[hostID] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for hostID := range seen {
		hosts = append(hosts, hostID)
	}
	sort.Strings(hosts)
	return hosts
}

func fleetMaterializeObservation(observations []fleet.Observation) (fleet.Observation, bool) {
	if len(observations) == 0 {
		return fleet.Observation{}, false
	}
	for _, observation := range observations {
		if strings.TrimSpace(observation.HostID) != "" {
			return observation, true
		}
	}
	return observations[0], true
}

func (b *tuiBackend) CreateWorktree(ctx context.Context, row dashboard.Row, branch string) (string, error) {
	if row.Entry == nil {
		return "", fmt.Errorf("no worktree selected")
	}
	return worktree.New(git.New(row.Entry.Path), b.cfg).Add(branch, "", true)
}

func (b *tuiBackend) MaterializeWorktree(ctx context.Context, row dashboard.Row) (string, error) {
	if row.Fleet == nil {
		return "", fmt.Errorf("no fleet worktree selected")
	}
	if row.Fleet.Local {
		return "", fmt.Errorf("worktree already materialized")
	}
	if row.Fleet.Kind != "branch" || strings.TrimSpace(row.Fleet.Branch) == "" {
		return "", fmt.Errorf("only branch worktrees can be materialized")
	}
	project, ok := b.projectForFleetInfo(row.Fleet)
	if !ok {
		return "", fmt.Errorf("no local project configured for %s", row.Fleet.ProjectIdentity)
	}
	path, err := worktree.New(git.New(project.Path), b.cfg).Add(row.Fleet.Branch, "", false)
	if err != nil {
		return "", err
	}
	if b.cfg != nil && b.cfg.Fleet.Enabled {
		_ = publishFleetBestEffort(ctx, b.cfg, newFleetManifestBuilder(), nil)
	}
	return path, nil
}

func (b *tuiBackend) projectForFleetInfo(info *dashboard.FleetInfo) (models.Project, bool) {
	if b == nil || b.cfg == nil || info == nil {
		return models.Project{}, false
	}
	for _, project := range b.cfg.Projects {
		if strings.TrimSpace(project.Path) == "" {
			continue
		}
		if strings.EqualFold(project.Repository, info.ProjectIdentity) {
			return project, true
		}
		if info.ProjectName != "" && strings.EqualFold(project.Name, info.ProjectName) {
			return project, true
		}
	}
	return models.Project{}, false
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
