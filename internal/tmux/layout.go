package tmux

import (
	"fmt"
	"sort"
	"strings"

	"go.kenn.io/kwt/pkg/models"
)

// ValidArranges returns the set of tmux select-layout presets kwt accepts.
func ValidArranges() map[string]bool {
	return map[string]bool{
		"even-horizontal": true,
		"even-vertical":   true,
		"tiled":           true,
		"main-vertical":   true,
		"main-horizontal": true,
	}
}

// BlankLayoutName is the reserved layout name for the blank session. It is
// valid anywhere a layout name is accepted and cannot name a preset.
const BlankLayoutName = "none"

// BlankLayout returns the implicit single-pane, plain-login-shell layout used
// when no preset is selected.
func BlankLayout() models.Layout {
	return models.Layout{Name: BlankLayoutName, Panes: []string{""}}
}

// BuildConstructionSequence returns the ordered, index-free tmux invocations
// that create and arrange the panes for a layout with N panes. Single-pane
// layouts emit no select-layout call. The new-session and split-window
// commands each print the new pane's stable ID (via -P -F '#{pane_id}'),
// which the runner captures to target panes by ID. It performs no I/O.
func BuildConstructionSequence(session, worktreeDir string, layout models.Layout) [][]string {
	seq := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "-c", worktreeDir},
	}
	for i := 1; i < len(layout.Panes); i++ {
		seq = append(seq,
			[]string{"split-window", "-P", "-F", "#{pane_id}", "-t", session, "-c", worktreeDir})
	}
	if len(layout.Panes) > 1 {
		seq = append(seq, []string{"select-layout", "-t", session, layout.Arrange})
	}
	return seq
}

// BuildPaneCommandSequence returns the tmux invocations that run each pane's
// command and set focus, given the captured pane IDs. paneIDs is in pane
// creation order with one entry per element of panes (len(paneIDs) ==
// len(panes)); paneIDs[i] is the ID of the pane for panes[i]. An empty command
// leaves that pane a plain login shell. It performs no I/O.
func BuildPaneCommandSequence(paneIDs, panes []string) [][]string {
	seq := make([][]string, 0, len(panes)*2+1)
	for i, cmd := range panes {
		if cmd == "" {
			continue
		}
		seq = append(seq,
			[]string{"send-keys", "-t", paneIDs[i], "-l", "--", cmd},
			[]string{"send-keys", "-t", paneIDs[i], "Enter"},
		)
	}
	if len(paneIDs) > 0 {
		seq = append(seq, []string{"select-pane", "-t", paneIDs[0]})
	}
	return seq
}

// ValidateLayouts checks arrange names, non-empty panes, agent references,
// the reserved blank name, and that a non-blank default resolves to a preset.
// Zero presets is valid: the blank session needs no configuration. Called
// before any workspace launch.
func ValidateLayouts(cfg models.LayoutsConfig, agents map[string]string) error {
	valid := ValidArranges()
	names := make(map[string]bool, len(cfg.Presets))
	for _, p := range cfg.Presets {
		if p.Name == BlankLayoutName {
			return fmt.Errorf("layout name %q is reserved for the blank session", BlankLayoutName)
		}
		if !valid[p.Arrange] {
			return fmt.Errorf("layout %q has invalid arrange %q; valid: %s",
				p.Name, p.Arrange, arrangeList())
		}
		if len(p.Panes) == 0 {
			return fmt.Errorf("layout %q has no panes", p.Name)
		}
		for _, pane := range p.Panes {
			if err := validatePaneCommand(p.Name, pane, agents); err != nil {
				return err
			}
		}
		names[p.Name] = true
	}
	if cfg.Default != "" && cfg.Default != BlankLayoutName && !names[cfg.Default] {
		return fmt.Errorf("layouts.default %q is not a defined preset (%s)",
			cfg.Default, presetList(cfg))
	}
	return nil
}

func validatePaneCommand(layoutName string, pane string, agents map[string]string) error {
	agent, ok := agentReference(pane)
	if !ok {
		return nil
	}
	if agent == "" {
		return fmt.Errorf("layout %q has empty agent reference", layoutName)
	}
	command, ok := agents[agent]
	if !ok {
		return fmt.Errorf("layout %q references unknown agent %q", layoutName, agent)
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("layout %q references agent %q with empty command", layoutName, agent)
	}
	return nil
}

// ResolvePaneCommands replaces agent:<name> pane references with the literal
// command configured in [agents]. Literal pane commands and empty shell panes
// are preserved.
func ResolvePaneCommands(layout models.Layout, agents map[string]string) (models.Layout, error) {
	resolved := layout
	resolved.Panes = append([]string(nil), layout.Panes...)
	for i, pane := range resolved.Panes {
		agent, ok := agentReference(pane)
		if !ok {
			continue
		}
		command, ok := agents[agent]
		if !ok {
			return models.Layout{}, fmt.Errorf("layout %q references unknown agent %q", layout.Name, agent)
		}
		if strings.TrimSpace(command) == "" {
			return models.Layout{}, fmt.Errorf("layout %q references agent %q with empty command", layout.Name, agent)
		}
		resolved.Panes[i] = command
	}
	return resolved, nil
}

func agentReference(pane string) (string, bool) {
	agent, ok := strings.CutPrefix(pane, "agent:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(agent), true
}

// FindPreset returns the preset with the given name or an error listing names.
func FindPreset(cfg models.LayoutsConfig, name string) (models.Layout, error) {
	for _, p := range cfg.Presets {
		if p.Name == name {
			p.Panes = append([]string(nil), p.Panes...)
			return p, nil
		}
	}
	return models.Layout{}, fmt.Errorf("unknown layout %q; available: %s", name, presetList(cfg))
}

// ResolveLayout applies the selection precedence: explicit --layout, then
// --select-layout (via selectFn), then the target repo default, then the
// global default. layoutFlag and selectFlag are mutually exclusive. An empty
// resolved name or the reserved name "none" yields the blank single-pane
// layout.
func ResolveLayout(
	cfg models.LayoutsConfig,
	layoutFlag string,
	selectFlag bool,
	targetDefault string,
	selectFn func([]models.Layout) (models.Layout, error),
) (models.Layout, error) {
	if layoutFlag != "" && selectFlag {
		return models.Layout{}, fmt.Errorf("--layout and --select-layout are mutually exclusive")
	}
	if selectFlag {
		return selectFn(append([]models.Layout{BlankLayout()}, cfg.Presets...))
	}
	name := layoutFlag
	if name == "" {
		name = targetDefault
	}
	if name == "" {
		name = cfg.Default
	}
	if name == "" || name == BlankLayoutName {
		return BlankLayout(), nil
	}
	return FindPreset(cfg, name)
}

func arrangeList() string {
	out := make([]string, 0, len(ValidArranges()))
	for a := range ValidArranges() {
		out = append(out, a)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func presetList(cfg models.LayoutsConfig) string {
	if len(cfg.Presets) == 0 {
		return "none"
	}
	seen := make(map[string]bool, len(cfg.Presets))
	out := make([]string, 0, len(cfg.Presets))
	for _, p := range cfg.Presets {
		if seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
