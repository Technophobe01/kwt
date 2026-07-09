package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/pkg/models"
)

func TestBuildConstructionSequenceQuad(t *testing.T) {
	layout := models.Layout{
		Name:    "quad",
		Arrange: "even-horizontal",
		Panes:   []string{"codex", "claude", "roborev tui", ""},
	}
	got := BuildConstructionSequence("kwt-workspace-x", "/wt", layout)

	want := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "kwt-workspace-x", "-c", "/wt"},
		{"split-window", "-P", "-F", "#{pane_id}", "-t", "kwt-workspace-x", "-c", "/wt"},
		{"split-window", "-P", "-F", "#{pane_id}", "-t", "kwt-workspace-x", "-c", "/wt"},
		{"split-window", "-P", "-F", "#{pane_id}", "-t", "kwt-workspace-x", "-c", "/wt"},
		{"select-layout", "-t", "kwt-workspace-x", "even-horizontal"},
	}
	assert.Equal(t, want, got)
}

func TestBuildConstructionSequenceSinglePane(t *testing.T) {
	layout := models.Layout{Arrange: "tiled", Panes: []string{"codex"}}
	got := BuildConstructionSequence("s", "/wt", layout)
	want := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "s", "-c", "/wt"},
	}
	assert.Equal(t, want, got, "single-pane layouts need no select-layout")
}

func TestBuildPaneCommandSequence(t *testing.T) {
	paneIDs := []string{"%1", "%2", "%3", "%4"}
	panes := []string{"codex", "claude", "roborev tui", ""}
	got := BuildPaneCommandSequence(paneIDs, panes)

	want := [][]string{
		{"send-keys", "-t", "%1", "-l", "--", "codex"},
		{"send-keys", "-t", "%1", "Enter"},
		{"send-keys", "-t", "%2", "-l", "--", "claude"},
		{"send-keys", "-t", "%2", "Enter"},
		{"send-keys", "-t", "%3", "-l", "--", "roborev tui"},
		{"send-keys", "-t", "%3", "Enter"},
		{"select-pane", "-t", "%1"},
	}
	assert.Equal(t, want, got)
}

func TestBuildPaneCommandSequenceSinglePane(t *testing.T) {
	got := BuildPaneCommandSequence([]string{"%7"}, []string{"codex"})
	want := [][]string{
		{"send-keys", "-t", "%7", "-l", "--", "codex"},
		{"send-keys", "-t", "%7", "Enter"},
		{"select-pane", "-t", "%7"},
	}
	assert.Equal(t, want, got)
}

func TestValidateLayouts(t *testing.T) {
	tests := []struct {
		name        string
		cfg         models.LayoutsConfig
		agents      map[string]string
		wantErr     bool
		errContains string
	}{
		{
			name: "good",
			cfg: models.LayoutsConfig{
				Default: "quad",
				Presets: []models.Layout{{Name: "quad", Arrange: "tiled", Panes: []string{"a"}}},
			},
		},
		{
			name:   "good agent reference",
			agents: map[string]string{"codex": "codex --profile kwt"},
			cfg: models.LayoutsConfig{
				Default: "quad",
				Presets: []models.Layout{
					{Name: "quad", Arrange: "tiled", Panes: []string{"agent:codex"}},
				},
			},
		},
		{
			name: "unknown agent reference",
			cfg: models.LayoutsConfig{
				Default: "quad",
				Presets: []models.Layout{
					{Name: "quad", Arrange: "tiled", Panes: []string{"agent:codex"}},
				},
			},
			wantErr:     true,
			errContains: "unknown agent",
		},
		{
			name: "invalid arrange",
			cfg: models.LayoutsConfig{
				Default: "quad",
				Presets: []models.Layout{{Name: "quad", Arrange: "nope", Panes: []string{"a"}}},
			},
			wantErr:     true,
			errContains: "even-horizontal",
		},
		{
			name: "empty panes",
			cfg: models.LayoutsConfig{
				Default: "quad",
				Presets: []models.Layout{{Name: "quad", Arrange: "tiled", Panes: nil}},
			},
			wantErr: true,
		},
		{
			name: "unknown default",
			cfg: models.LayoutsConfig{
				Default: "ghost",
				Presets: []models.Layout{{Name: "quad", Arrange: "tiled", Panes: []string{"a"}}},
			},
			wantErr: true,
		},
		{
			name: "zero presets is valid",
			cfg:  models.LayoutsConfig{},
		},
		{
			name: "default none is valid without presets",
			cfg:  models.LayoutsConfig{Default: "none"},
		},
		{
			name: "preset named none is reserved",
			cfg: models.LayoutsConfig{
				Presets: []models.Layout{{Name: "none", Arrange: "tiled", Panes: []string{"a"}}},
			},
			wantErr:     true,
			errContains: "reserved",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayouts(tc.cfg, tc.agents)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.errContains != "" {
				assert.Contains(t, err.Error(), tc.errContains)
			}
		})
	}
}

func TestResolvePaneCommandsExpandsAgentReferences(t *testing.T) {
	layout := models.Layout{
		Name:    "quad",
		Arrange: "tiled",
		Panes:   []string{"agent:codex", "agent:claude", "make test", ""},
	}
	agents := map[string]string{
		"codex":  "codex --profile kwt",
		"claude": "claude --model sonnet",
	}

	got, err := ResolvePaneCommands(layout, agents)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"codex --profile kwt",
		"claude --model sonnet",
		"make test",
		"",
	}, got.Panes)
	assert.Equal(t, []string{"agent:codex", "agent:claude", "make test", ""}, layout.Panes,
		"must not mutate the configured layout")
}

func TestResolveLayout(t *testing.T) {
	cfg := models.LayoutsConfig{
		Default: "quad",
		Presets: []models.Layout{
			{Name: "quad", Arrange: "even-horizontal", Panes: []string{"a"}},
			{Name: "focus", Arrange: "main-vertical", Panes: []string{"a"}},
		},
	}

	tests := []struct {
		name          string
		layoutFlag    string
		selectFlag    bool
		targetDefault string
		useSelectFn   bool // true: selectFn returns cfg.Presets[1] instead of failing the test
		wantErr       bool
		errContains   string
		wantName      string
	}{
		{
			name:       "explicit layout flag wins",
			layoutFlag: "focus",
			wantName:   "focus",
		},
		{
			name:          "target default wins over global default",
			targetDefault: "focus",
			wantName:      "focus",
		},
		{
			name:     "global default is the fallback",
			wantName: "quad",
		},
		{
			name:        "unknown layout name errors",
			layoutFlag:  "ghost",
			wantErr:     true,
			errContains: "quad",
		},
		{
			name:       "layout and select-layout are mutually exclusive",
			layoutFlag: "focus",
			selectFlag: true,
			wantErr:    true,
		},
		{
			name:          "select overrides target and global default",
			selectFlag:    true,
			targetDefault: "quad",
			useSelectFn:   true,
			wantName:      "focus",
		},
		{
			name:       "layout flag none yields blank",
			layoutFlag: "none",
			wantName:   "none",
		},
		{
			name:          "target default none yields blank",
			targetDefault: "none",
			wantName:      "none",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selectFn := func([]models.Layout) (models.Layout, error) {
				t.Fatal("selectFn should not run")
				return models.Layout{}, nil
			}
			if tc.useSelectFn {
				selectFn = func([]models.Layout) (models.Layout, error) { return cfg.Presets[1], nil }
			}

			got, err := ResolveLayout(cfg, tc.layoutFlag, tc.selectFlag, tc.targetDefault, selectFn)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, got.Name)
		})
	}
}

func TestResolveLayoutBlankWhenNothingConfigured(t *testing.T) {
	got, err := ResolveLayout(models.LayoutsConfig{}, "", false, "", nil)
	require.NoError(t, err)
	assert.Equal(t, BlankLayout(), got)
	require.Len(t, got.Panes, 1)
	assert.Empty(t, got.Panes[0], "blank pane must be a plain login shell")
}

func TestResolveLayoutSelectPrependsBlank(t *testing.T) {
	cfg := models.LayoutsConfig{
		Presets: []models.Layout{{Name: "quad", Arrange: "tiled", Panes: []string{"a"}}},
	}
	var offered []string
	selectFn := func(ls []models.Layout) (models.Layout, error) {
		for _, l := range ls {
			offered = append(offered, l.Name)
		}
		return ls[0], nil
	}
	got, err := ResolveLayout(cfg, "", true, "", selectFn)
	require.NoError(t, err)
	assert.Equal(t, []string{"none", "quad"}, offered)
	assert.Equal(t, BlankLayout(), got)
}
