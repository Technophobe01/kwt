package fleet

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestJSONWireShape(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1,
		HostID:        "host-a",
		Host:          HostInfo{Hostname: "Host-A", Platform: "darwin/arm64"},
		ObservedAt:    time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
		Projects:      []ProjectManifest{{Identity: "github.com/kenn-io/kwt", Name: "kwt", LocalRoot: "/home/user-a/code/kwt", RemoteURL: "git@github.com:kenn-io/kwt.git"}},
		Worktrees: []WorktreeManifest{{
			ProjectIdentity: "github.com/kenn-io/kwt",
			Kind:            "branch",
			Ref:             "feature/fleet",
			Branch:          "feature/fleet",
			Path:            "/tmp/feature",
			Head:            "abc",
			Status:          ChangeStatus{Modified: 1, Untracked: 2},
		}},
	}

	body, err := json.Marshal(manifest)
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, `"schema_version":1`)
	assert.Contains(t, text, `"host_id":"host-a"`)
	assert.Contains(t, text, `"project_identity":"github.com/kenn-io/kwt"`)
	assert.Contains(t, text, `"ahead":0`)
	assert.Contains(t, text, `"modified":1`)
	assert.NotContains(t, text, "SchemaVersion")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	worktrees, ok := decoded["worktrees"].([]any)
	require.True(t, ok, "worktrees should decode as an array")
	require.Len(t, worktrees, 1)
	first, ok := worktrees[0].(map[string]any)
	require.True(t, ok, "worktree should decode as an object")
	status, ok := first["status"].(map[string]any)
	require.True(t, ok, "status should decode as an object")
	assert.NotContains(t, status, "ahead", "ahead belongs beside status, not inside status")
	assert.NotContains(t, status, "behind", "behind belongs beside status, not inside status")
}

func TestFleetStateJSONWireShape(t *testing.T) {
	state := FleetState{
		SchemaVersion: 1,
		StateVersion:  "sha256:abc123",
		Hosts:         []HostState{{HostID: "host-a", Hostname: "Host-A", Platform: "darwin/arm64"}},
		Rows: []FleetRow{{
			ProjectIdentity: "github.com/kenn-io/kwt",
			ProjectName:     "kwt",
			Kind:            "branch",
			Ref:             "feature/fleet",
			Branch:          "feature/fleet",
			Observations:    []Observation{{HostID: "host-a", Head: "abc", Status: ChangeStatus{Staged: 1}}},
		}},
	}

	body, err := json.Marshal(state)
	require.NoError(t, err)
	text := string(body)
	assert.Contains(t, text, `"state_version":"sha256:abc123"`)
	assert.Contains(t, text, `"observations"`)
	assert.Contains(t, text, `"staged":1`)
	assert.NotContains(t, text, "StateVersion")
}
