package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/internal/git"
	"go.kenn.io/kwt/pkg/models"
)

func TestShouldLaunch(t *testing.T) {
	cases := []struct {
		name             string
		autoLaunch       bool
		layoutFlagPassed bool
		noLaunch         bool
		want             bool
		wantErr          bool
	}{
		{"default on", true, false, false, true, false},
		{"default off", false, false, false, false, false},
		{"flag forces launch when default off", false, true, false, true, false},
		{"no-launch suppresses", true, false, true, false, false},
		{"no-launch plus flag errors", false, true, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shouldLaunch(tc.autoLaunch, tc.layoutFlagPassed, tc.noLaunch)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPrepareLaunchUsesLocalRepositoryFallback(t *testing.T) {
	putFakeTmuxOnPath(t)
	oldLayout, oldSelectLayout := addLayout, addSelectLayout
	t.Cleanup(func() {
		addLayout = oldLayout
		addSelectLayout = oldSelectLayout
	})
	addLayout = ""
	addSelectLayout = false

	repoPath := newTUITestRepo(t)
	ctx := &CommandContext{
		Config: &models.Config{
			Layouts: models.LayoutsConfig{
				Default: "shell",
				Presets: []models.Layout{{
					Name:    "shell",
					Arrange: "tiled",
					Panes:   []string{""},
				}},
			},
		},
		Git: git.New(repoPath),
	}

	layout, info, err := prepareLaunch(ctx)

	require.NoError(t, err)
	assert.Equal(t, "shell", layout.Name)
	require.NotNil(t, info)
	assert.Equal(t, filepath.Base(repoPath), info.Repository)
	assert.True(t, strings.HasPrefix(info.FullPath, "local/"), info.FullPath)
}

func putFakeTmuxOnPath(t *testing.T) {
	t.Helper()

	name := "tmux"
	if runtime.GOOS == "windows" {
		name = "tmux.exe"
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(""), 0755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
