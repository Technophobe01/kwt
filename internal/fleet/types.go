package fleet

import (
	"fmt"
	"strings"
	"time"
)

const (
	// ManifestSchemaVersion is the current fleet manifest wire schema version.
	ManifestSchemaVersion = 1
	// StateSchemaVersion is the current fleet state wire schema version.
	StateSchemaVersion = 1
)

// Manifest is the versioned host report published to the fleet hub.
type Manifest struct {
	SchemaVersion int                `json:"schema_version"`
	HostID        string             `json:"host_id"`
	Host          HostInfo           `json:"host"`
	ObservedAt    time.Time          `json:"observed_at"`
	Projects      []ProjectManifest  `json:"projects"`
	Worktrees     []WorktreeManifest `json:"worktrees"`
}

// HostInfo identifies the machine that produced a manifest.
type HostInfo struct {
	Hostname string `json:"hostname"`
	Platform string `json:"platform"`
}

// ProjectManifest describes one logical project observed on a host.
// Raw git remote URLs are deliberately excluded: they can embed credentials
// and must never be published to the hub. Identity carries the normalized
// repository identity instead.
type ProjectManifest struct {
	Identity  string `json:"identity"`
	Name      string `json:"name"`
	LocalRoot string `json:"local_root"`
}

// ChangeStatus is the dirty-state subset carried inside fleet status objects.
type ChangeStatus struct {
	Modified  int `json:"modified"`
	Added     int `json:"added"`
	Deleted   int `json:"deleted"`
	Untracked int `json:"untracked"`
	Staged    int `json:"staged"`
	Conflicts int `json:"conflicts"`
}

// WorktreeManifest describes one worktree observed in a host manifest.
type WorktreeManifest struct {
	ProjectIdentity string       `json:"project_identity"`
	Kind            string       `json:"kind"`
	Ref             string       `json:"ref"`
	Branch          string       `json:"branch,omitempty"`
	Path            string       `json:"path"`
	Head            string       `json:"head"`
	HeadTime        time.Time    `json:"head_time"`
	Upstream        string       `json:"upstream,omitempty"`
	Ahead           int          `json:"ahead"`
	Behind          int          `json:"behind"`
	Status          ChangeStatus `json:"status"`
	LastActivity    time.Time    `json:"last_activity"`
	IsMain          bool         `json:"is_main"`
}

// FleetState is the hub-computed grouped fleet view.
type FleetState struct {
	SchemaVersion int         `json:"schema_version"`
	StateVersion  string      `json:"state_version"`
	Hosts         []HostState `json:"hosts"`
	Rows          []FleetRow  `json:"rows"`
	Warnings      []Warning   `json:"warnings,omitempty"`
}

// HostState identifies a host present in the hub state response.
type HostState struct {
	HostID     string    `json:"host_id"`
	Hostname   string    `json:"hostname"`
	Platform   string    `json:"platform"`
	ObservedAt time.Time `json:"observed_at"`
}

// FleetRow groups observations by logical project and worktree identity.
type FleetRow struct {
	ProjectIdentity string        `json:"project_identity"`
	ProjectName     string        `json:"project_name"`
	Kind            string        `json:"kind"`
	Ref             string        `json:"ref"`
	Branch          string        `json:"branch,omitempty"`
	Observations    []Observation `json:"observations"`
}

// Observation is one host's report for a fleet row.
type Observation struct {
	HostID       string       `json:"host_id"`
	Path         string       `json:"path"`
	Head         string       `json:"head"`
	HeadTime     time.Time    `json:"head_time"`
	Upstream     string       `json:"upstream,omitempty"`
	Ahead        int          `json:"ahead"`
	Behind       int          `json:"behind"`
	Status       ChangeStatus `json:"status"`
	LastActivity time.Time    `json:"last_activity"`
	ObservedAt   time.Time    `json:"observed_at"`
	IsMain       bool         `json:"is_main"`
}

// Warning describes a non-fatal fleet state issue.
type Warning struct {
	Code    string `json:"code"`
	HostID  string `json:"host_id,omitempty"`
	Message string `json:"message"`
}

// String renders a warning for terminal display.
func (w Warning) String() string {
	text := strings.TrimSpace(w.Message)
	if text == "" {
		text = w.Code
	}
	if hostID := strings.TrimSpace(w.HostID); hostID != "" {
		return fmt.Sprintf("%s (host %s)", text, hostID)
	}
	return text
}
