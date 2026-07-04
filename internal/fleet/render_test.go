package fleet

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderRowsMarksMissingMaterializedAndDiffering(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	state := FleetState{Rows: []FleetRow{{
		ProjectIdentity: "github.com/kenn-io/kwt",
		ProjectName:     "kwt",
		Kind:            "branch",
		Ref:             "feature/fleet",
		Branch:          "feature/fleet",
		Observations: []Observation{
			{HostID: "host-a", Head: "aaa", ObservedAt: now.Add(-time.Minute)},
			{HostID: "host-b", Head: "bbb", Status: ChangeStatus{Modified: 1}, ObservedAt: now.Add(-2 * time.Minute)},
		},
	}}}

	rows := BuildStatusRows(state, "host-a", now)
	require.Len(t, rows, 1)
	assert.Equal(t, "materialized", rows[0].Local)
	assert.Contains(t, rows[0].Sync, "differs from host-b")
	assert.Contains(t, rows[0].Dirty, "host-b")

	rows = BuildStatusRows(state, "air", now)
	require.Len(t, rows, 1)
	assert.Equal(t, "missing", rows[0].Local)
}

func TestRenderStatusTableWritesFleetRows(t *testing.T) {
	var out bytes.Buffer
	err := RenderStatusTable(&out, []StatusRow{{
		Project:   "kwt",
		Ref:       "feature/fleet",
		Local:     "missing",
		Hosts:     "host-a, host-b",
		Sync:      "same",
		Dirty:     "clean",
		Freshness: "1 min ago",
	}})

	require.NoError(t, err)
	assert.Contains(t, out.String(), "PROJECT")
	assert.Contains(t, out.String(), "kwt")
	assert.Contains(t, out.String(), "feature/fleet")
	assert.Contains(t, out.String(), "missing")
}

func TestRenderRowsFormatsDirtyCounts(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	state := FleetState{Rows: []FleetRow{{
		ProjectName: "kwt",
		Ref:         "feature/fleet",
		Observations: []Observation{{
			HostID:     "host-b",
			Head:       "aaa",
			Status:     ChangeStatus{Modified: 2, Conflicts: 3},
			ObservedAt: now,
		}},
	}}}

	rows := BuildStatusRows(state, "host-b", now)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Dirty, "2 modified")
	assert.NotContains(t, rows[0].Dirty, "modifieds")
	assert.Contains(t, rows[0].Dirty, "3 conflicts")
}
