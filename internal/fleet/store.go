package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store persists fleet manifests and returns the grouped fleet state.
type Store interface {
	Put(ctx context.Context, manifest Manifest) error
	Delete(ctx context.Context, hostID string) error
	State(ctx context.Context) (FleetState, error)
}

// FileStore stores the latest manifest for each host in a JSON file.
type FileStore struct {
	path string
	mu   sync.Mutex
}

type storeFile struct {
	Hosts    map[string]Manifest `json:"hosts"`
	Warnings []Warning           `json:"warnings,omitempty"`
}

// NewFileStore creates a file-backed fleet store.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Put stores manifest as the latest observation for its host.
func (s *FileStore) Put(ctx context.Context, manifest Manifest) error {
	ctx = storeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateStoreManifest(manifest); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := s.read(ctx)
	if err != nil {
		return err
	}
	if existing, ok := file.Hosts[manifest.HostID]; ok && hostInfoChanged(existing, manifest) {
		file.Warnings = upsertWarning(file.Warnings, Warning{
			Code:   "host_id_collision",
			HostID: manifest.HostID,
			Message: fmt.Sprintf(
				"host ID %q changed host identity from %q/%q to %q/%q",
				manifest.HostID,
				existing.Host.Hostname,
				existing.Host.Platform,
				manifest.Host.Hostname,
				manifest.Host.Platform,
			),
		})
	}
	file.Hosts[manifest.HostID] = manifest
	return s.write(ctx, file)
}

// Delete removes a host from the store.
func (s *FileStore) Delete(ctx context.Context, hostID string) error {
	ctx = storeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := NormalizeHostID(hostID)
	if err != nil {
		return err
	}
	if normalized != hostID {
		return fmt.Errorf("host ID %q must be normalized as %q", hostID, normalized)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := s.read(ctx)
	if err != nil {
		return err
	}

	changed := false
	if _, ok := file.Hosts[hostID]; ok {
		delete(file.Hosts, hostID)
		changed = true
	}
	warnings := file.Warnings[:0]
	for _, warning := range file.Warnings {
		if warning.HostID == hostID {
			changed = true
			continue
		}
		warnings = append(warnings, warning)
	}
	file.Warnings = warnings
	if !changed {
		return nil
	}
	return s.write(ctx, file)
}

// State returns the current grouped fleet state.
func (s *FileStore) State(ctx context.Context) (FleetState, error) {
	ctx = storeContext(ctx)
	if err := ctx.Err(); err != nil {
		return FleetState{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.read(ctx)
	if err != nil {
		return FleetState{}, err
	}
	if err := ctx.Err(); err != nil {
		return FleetState{}, err
	}
	return buildFleetState(file)
}

func (s *FileStore) read(ctx context.Context) (storeFile, error) {
	if err := ctx.Err(); err != nil {
		return storeFile{}, err
	}
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return newStoreFile(), nil
	}
	if err != nil {
		return storeFile{}, err
	}
	if err := ctx.Err(); err != nil {
		return storeFile{}, err
	}

	var file storeFile
	if err := json.Unmarshal(body, &file); err != nil {
		return storeFile{}, fmt.Errorf("read fleet store: %w", err)
	}
	if file.Hosts == nil {
		file.Hosts = map[string]Manifest{}
	}
	sortWarnings(file.Warnings)
	return file, nil
}

func (s *FileStore) write(ctx context.Context, file storeFile) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if file.Hosts == nil {
		file.Hosts = map[string]Manifest{}
	}
	sortWarnings(file.Warnings)

	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempName)
		}
	}()

	if _, err = temp.Write(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(tempName, s.path); err != nil {
		return err
	}
	return nil
}

func buildFleetState(file storeFile) (FleetState, error) {
	state := FleetState{
		SchemaVersion: StateSchemaVersion,
		Hosts:         make([]HostState, 0, len(file.Hosts)),
		Rows:          []FleetRow{},
		Warnings:      append([]Warning(nil), file.Warnings...),
	}

	hostIDs := make([]string, 0, len(file.Hosts))
	for hostID := range file.Hosts {
		hostIDs = append(hostIDs, hostID)
	}
	sort.Strings(hostIDs)

	rowsByKey := map[rowKey]*FleetRow{}
	for _, hostID := range hostIDs {
		manifest := file.Hosts[hostID]
		state.Hosts = append(state.Hosts, HostState{
			HostID:     manifest.HostID,
			Hostname:   manifest.Host.Hostname,
			Platform:   manifest.Host.Platform,
			ObservedAt: manifest.ObservedAt,
		})

		projectNames := projectNamesByIdentity(manifest.Projects)
		for _, worktree := range manifest.Worktrees {
			key := rowKey{
				projectIdentity: worktree.ProjectIdentity,
				kind:            worktree.Kind,
				ref:             worktree.Ref,
			}
			row := rowsByKey[key]
			projectName := projectNames[worktree.ProjectIdentity]
			if row == nil {
				row = &FleetRow{
					ProjectIdentity: worktree.ProjectIdentity,
					ProjectName:     projectNameOrIdentity(worktree.ProjectIdentity, projectName),
					Kind:            worktree.Kind,
					Ref:             worktree.Ref,
					Branch:          worktree.Branch,
					Observations:    []Observation{},
				}
				rowsByKey[key] = row
			} else {
				updateRowProjectName(row, projectName)
				if worktree.Branch != "" && (row.Branch == "" || worktree.Branch < row.Branch) {
					row.Branch = worktree.Branch
				}
			}

			row.Observations = append(row.Observations, Observation{
				HostID:       manifest.HostID,
				Path:         worktree.Path,
				Head:         worktree.Head,
				HeadTime:     worktree.HeadTime,
				Upstream:     worktree.Upstream,
				Ahead:        worktree.Ahead,
				Behind:       worktree.Behind,
				Status:       worktree.Status,
				LastActivity: worktree.LastActivity,
				ObservedAt:   manifest.ObservedAt,
				IsMain:       worktree.IsMain,
			})
		}
	}

	keys := make([]rowKey, 0, len(rowsByKey))
	for key := range rowsByKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareRowKey(keys[i], keys[j]) < 0
	})
	for _, key := range keys {
		row := *rowsByKey[key]
		sort.Slice(row.Observations, func(i, j int) bool {
			left := row.Observations[i]
			right := row.Observations[j]
			if left.HostID != right.HostID {
				return left.HostID < right.HostID
			}
			return left.Path < right.Path
		})
		state.Rows = append(state.Rows, row)
	}

	sortWarnings(state.Warnings)
	version, err := fleetStateVersion(state)
	if err != nil {
		return FleetState{}, err
	}
	state.StateVersion = version
	return state, nil
}

func validateStoreManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	normalized, err := NormalizeHostID(manifest.HostID)
	if err != nil {
		return err
	}
	if normalized != manifest.HostID {
		return fmt.Errorf("host ID %q must be normalized as %q", manifest.HostID, normalized)
	}
	for i, project := range manifest.Projects {
		if err := validateCanonicalIdentity(fmt.Sprintf("project %d identity", i), project.Identity); err != nil {
			return err
		}
	}
	for i, worktree := range manifest.Worktrees {
		if err := validateCanonicalIdentity(fmt.Sprintf("worktree %d project identity", i), worktree.ProjectIdentity); err != nil {
			return err
		}
		if strings.TrimSpace(worktree.Kind) == "" {
			return fmt.Errorf("worktree %d kind is required", i)
		}
		if !isSupportedWorktreeKind(worktree.Kind) {
			return fmt.Errorf("worktree %d kind %q is unsupported", i, worktree.Kind)
		}
		if strings.TrimSpace(worktree.Ref) == "" {
			return fmt.Errorf("worktree %d ref is required", i)
		}
	}
	return nil
}

// validateCanonicalIdentity rejects identities that are not already in
// normalized form, so raw remote URLs (which can embed credentials) are never
// stored or echoed back in fleet state.
func validateCanonicalIdentity(field string, identity string) error {
	if strings.TrimSpace(identity) == "" {
		return fmt.Errorf("%s is required", field)
	}
	normalized, err := NormalizeRepositoryIdentity(identity)
	if err != nil {
		return fmt.Errorf("%s %q is not a canonical repository identity: %w", field, identity, err)
	}
	if normalized != identity {
		return fmt.Errorf("%s %q must be normalized as %q", field, identity, normalized)
	}
	return nil
}

func isSupportedWorktreeKind(kind string) bool {
	switch kind {
	case "branch", "detached":
		return true
	default:
		return false
	}
}

func fleetStateVersion(state FleetState) (string, error) {
	state.StateVersion = ""
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func newStoreFile() storeFile {
	return storeFile{Hosts: map[string]Manifest{}}
}

func storeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func hostInfoChanged(left Manifest, right Manifest) bool {
	return left.Host.Hostname != right.Host.Hostname || left.Host.Platform != right.Host.Platform
}

func upsertWarning(warnings []Warning, warning Warning) []Warning {
	for i := range warnings {
		if warnings[i].Code == warning.Code && warnings[i].HostID == warning.HostID {
			warnings[i] = warning
			sortWarnings(warnings)
			return warnings
		}
	}
	warnings = append(warnings, warning)
	sortWarnings(warnings)
	return warnings
}

func sortWarnings(warnings []Warning) {
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Code != warnings[j].Code {
			return warnings[i].Code < warnings[j].Code
		}
		if warnings[i].HostID != warnings[j].HostID {
			return warnings[i].HostID < warnings[j].HostID
		}
		return warnings[i].Message < warnings[j].Message
	})
}

func projectNamesByIdentity(projects []ProjectManifest) map[string]string {
	names := make(map[string]string, len(projects))
	for _, project := range projects {
		name := strings.TrimSpace(project.Name)
		if name == "" {
			continue
		}
		current := names[project.Identity]
		if current == "" || name < current {
			names[project.Identity] = name
		}
	}
	return names
}

func projectNameOrIdentity(projectIdentity string, projectName string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return projectIdentity
	}
	return projectName
}

func updateRowProjectName(row *FleetRow, projectName string) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return
	}
	if row.ProjectName == row.ProjectIdentity || projectName < row.ProjectName {
		row.ProjectName = projectName
	}
}

type rowKey struct {
	projectIdentity string
	kind            string
	ref             string
}

func compareRowKey(left rowKey, right rowKey) int {
	if left.projectIdentity != right.projectIdentity {
		return strings.Compare(left.projectIdentity, right.projectIdentity)
	}
	if left.kind != right.kind {
		return strings.Compare(left.kind, right.kind)
	}
	return strings.Compare(left.ref, right.ref)
}
