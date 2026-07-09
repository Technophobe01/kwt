# Blank Default Session Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a blank single-pane tmux session the zero-config default for workspace launches, with layout presets as pure opt-in and the reserved name `none` meaning "blank" everywhere a layout name is accepted.

**Architecture:** Blank is a degenerate `models.Layout{Name: "none", Panes: []string{""}}` that flows through the existing resolution → construction → attach pipeline unchanged. `ResolveLayout` returns it when the resolved name is empty or `none`; `ValidateLayouts` stops requiring presets; config migration stops force-writing defaults. See spec: `docs/superpowers/specs/2026-07-08-blank-default-session-design.md`.

**Tech Stack:** Go, cobra, viper, bubbletea, go-fuzzyfinder, testify.

## Global Constraints

- Reserved layout name is exactly `"none"` (constant `BlankLayoutName` in `internal/tmux`).
- Defining a preset named `none` is a config validation error.
- Existing config files are respected as-is: migration must not write `layouts.default` or seed presets. `auto_launch_on_add` backfill stays.
- Deliberate breakage (do NOT "fix"): a hand-authored config with `layouts.default = "quad"` and zero presets errors with `layouts.default "quad" is not a defined preset`.
- Run tests with `go test ./internal/<pkg>/...`; full gate is `make test` and `make lint` (golangci-lint).
- Commit after every task; imperative mood, ≤72-char subject.

---

### Task 1: Blank layout core (`internal/tmux/layout.go`)

**Files:**
- Modify: `internal/tmux/layout.go`
- Test: `internal/tmux/layout_test.go`

**Interfaces:**
- Consumes: existing `models.Layout`, `models.LayoutsConfig`.
- Produces: `const BlankLayoutName = "none"` and `func BlankLayout() models.Layout` — Tasks 2 and 3 reference both. `ResolveLayout`, `ValidateLayouts`, `BuildConstructionSequence` keep their signatures.

- [ ] **Step 1: Write the failing tests**

In `internal/tmux/layout_test.go`, replace `TestBuildConstructionSequenceSinglePane` (line 29) with:

```go
func TestBuildConstructionSequenceSinglePane(t *testing.T) {
	layout := models.Layout{Arrange: "tiled", Panes: []string{"codex"}}
	got := BuildConstructionSequence("s", "/wt", layout)
	want := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "s", "-c", "/wt"},
	}
	assert.Equal(t, want, got, "single-pane layouts need no select-layout")
}
```

In `TestValidateLayouts`, delete nothing, and add these cases to the `tests` slice:

```go
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
```

In `TestResolveLayout`, add these cases to the `tests` slice (the shared `cfg` at the top of the test keeps `Default: "quad"`):

```go
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
```

And add two standalone tests after `TestResolveLayout`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tmux/ -run 'TestValidateLayouts|TestResolveLayout|TestBuildConstructionSequenceSinglePane' -v`
Expected: FAIL — `BlankLayout` undefined (compile error).

- [ ] **Step 3: Implement**

In `internal/tmux/layout.go`:

Add after `ValidArranges`:

```go
// BlankLayoutName is the reserved layout name for the blank session. It is
// valid anywhere a layout name is accepted and cannot name a preset.
const BlankLayoutName = "none"

// BlankLayout returns the implicit single-pane, plain-login-shell layout used
// when no preset is selected.
func BlankLayout() models.Layout {
	return models.Layout{Name: BlankLayoutName, Panes: []string{""}}
}
```

In `BuildConstructionSequence`, wrap the trailing `select-layout` so single-pane layouts skip it:

```go
	if len(layout.Panes) > 1 {
		seq = append(seq, []string{"select-layout", "-t", session, layout.Arrange})
	}
	return seq
```

Also update its doc comment's second sentence to note: "Single-pane layouts emit no select-layout call."

Replace `ValidateLayouts` with:

```go
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
```

In `ResolveLayout`: prepend blank to the picker list and return blank for empty/`none` names. Replace the body after the mutual-exclusion check with:

```go
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
```

Update `ResolveLayout`'s doc comment to add: "An empty resolved name or the reserved name \"none\" yields the blank single-pane layout."

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/tmux/... -v`
Expected: PASS (runner tests are unaffected: their single-pane cases never assert construction calls, and multi-pane cases keep `select-layout`).

- [ ] **Step 5: Commit**

```bash
git add internal/tmux/layout.go internal/tmux/layout_test.go
git commit -m "Add reserved blank layout; make presets optional"
```

---

### Task 2: Picker label for the blank entry (`internal/finder`)

**Files:**
- Modify: `internal/finder/finder.go` (`SelectLayout`, ~line 211)
- Test: `internal/finder/finder_test.go`

**Interfaces:**
- Consumes: `tmux.BlankLayoutName`, `tmux.BlankLayout()` from Task 1 (`internal/finder/finder.go` already imports `go.kenn.io/kwt/internal/tmux`).
- Produces: unexported `layoutItemLabel(l models.Layout) string`; `SelectLayout` signature unchanged.

- [ ] **Step 1: Write the failing test**

Add to `internal/finder/finder_test.go` (add `go.kenn.io/kwt/internal/tmux` and `go.kenn.io/kwt/pkg/models` to imports if absent):

```go
func TestLayoutItemLabel(t *testing.T) {
	assert.Equal(t, "none (blank session)", layoutItemLabel(tmux.BlankLayout()))
	assert.Equal(t, "quad (tiled, 2 panes)",
		layoutItemLabel(models.Layout{Name: "quad", Arrange: "tiled", Panes: []string{"a", ""}}))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/finder/ -run TestLayoutItemLabel -v`
Expected: FAIL — `layoutItemLabel` undefined.

- [ ] **Step 3: Implement**

In `internal/finder/finder.go`, replace the inline label closure in `SelectLayout` and add the helper:

```go
	idx, err := fuzzyfinder.Find(
		layouts,
		func(i int) string { return layoutItemLabel(layouts[i]) },
		fuzzyfinder.WithPromptString("Select layout> "),
	)
```

```go
// layoutItemLabel formats one layout picker row. The reserved blank layout
// has no arrange or meaningful pane count, so it gets a descriptive label
// instead of "none (, 1 panes)".
func layoutItemLabel(l models.Layout) string {
	if l.Name == tmux.BlankLayoutName {
		return l.Name + " (blank session)"
	}
	return fmt.Sprintf("%s (%s, %d panes)", l.Name, l.Arrange, len(l.Panes))
}
```

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/finder/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/finder/finder.go internal/finder/finder_test.go
git commit -m "Label blank layout entry in the layout picker"
```

---

### Task 3: TUI blank entry (`internal/cmd/tui_backend.go`, `internal/tui/model.go`)

**Files:**
- Modify: `internal/cmd/tui_backend.go` (`LayoutNames`, ~line 1182)
- Modify: `internal/tui/model.go` (`cycleLayout`, ~line 542)
- Test: `internal/cmd/tui_test.go`

**Interfaces:**
- Consumes: `tmux.BlankLayoutName` from Task 1.
- Produces: `LayoutNames()` now returns `["none", <presets...>]`. The TUI hands the selected name to `resolveLayout` → `tmux.ResolveLayout` as the layout flag, so `none` resolves to blank with no other TUI change.

**Refinement over spec:** the spec says `cycleLayout`'s empty-list branch is deleted outright. Deleting the guard entirely would make `m.layouts[0]` panic if a `Backend` implementation ever returns an empty slice (the model is interface-driven; test fakes can do this). Keep a bare no-op guard; delete only the now-impossible "no layouts configured" message.

- [ ] **Step 1: Write the failing test**

Add to `internal/cmd/tui_test.go` (`newTUIBackend` takes `*models.Config`, tui_backend.go:45):

```go
func TestTUIBackendLayoutNamesPrependsNone(t *testing.T) {
	backend := newTUIBackend(&models.Config{
		Layouts: models.LayoutsConfig{
			Presets: []models.Layout{{Name: "quad", Arrange: "tiled", Panes: []string{""}}},
		},
	})
	assert.Equal(t, []string{"none", "quad"}, backend.LayoutNames())
}

func TestTUIBackendLayoutNamesBlankOnly(t *testing.T) {
	backend := newTUIBackend(&models.Config{})
	assert.Equal(t, []string{"none"}, backend.LayoutNames())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cmd/ -run TestTUIBackendLayoutNames -v`
Expected: FAIL — got `["quad"]` / `[]`, want `["none", "quad"]` / `["none"]`.

- [ ] **Step 3: Implement**

In `internal/cmd/tui_backend.go`:

```go
// LayoutNames returns the names the TUI layout cycler offers: the reserved
// blank session first, then the configured presets.
func (b *tuiBackend) LayoutNames() []string {
	names := make([]string, 0, len(b.cfg.Layouts.Presets)+1)
	names = append(names, tmux.BlankLayoutName)
	for _, layout := range b.cfg.Layouts.Presets {
		names = append(names, layout.Name)
	}
	return names
}
```

In `internal/tui/model.go` `cycleLayout`, replace the message branch:

```go
	if len(m.layouts) == 0 {
		return m, nil
	}
```

(delete the `m.message = "no layouts configured"` line; everything else in `cycleLayout` is unchanged).

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/cmd/... ./internal/tui/... -v`
Expected: PASS. `TestModelCyclesLayoutSelection` (model_test.go:350) drives the cycle through a fake backend and is unaffected.

- [ ] **Step 5: Commit**

```bash
git add internal/cmd/tui_backend.go internal/cmd/tui_test.go internal/tui/model.go
git commit -m "Offer blank session in TUI layout cycler"
```

---

### Task 4: Config defaults and migration (`internal/config/config.go`)

**Files:**
- Modify: `internal/config/config.go` (`defaultLayouts` ~line 37, migration block ~lines 287-301, `defaultConfigTOML` ~line 353)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: fresh configs have no `layouts.default`; `defaultLayouts()` is deleted (its only caller was the migration block).

- [ ] **Step 1: Update existing tests and add the migration test**

In `internal/config/config_test.go`:

`TestDefaultLayoutsConfig` (line 1393): replace `assert.Equal(t, "quad", cfg.Layouts.Default)` with:

```go
	assert.Empty(t, cfg.Layouts.Default, "fresh config must default to a blank session")
```

All preset assertions stay: the fresh template still ships quad/grid/focus/stack.

`TestInitBackfillsWorkspaceConfigIntoExistingConfig` (line 1445): the existing config has no `[layouts]`, and migration no longer seeds them. Replace the three layout assertions (`Equal "quad"` / `True AutoLaunchOnAdd` / `NotEmpty Presets`) with:

```go
	assert.Empty(t, cfg.Layouts.Default, "migration must not write a default layout")
	assert.True(t, cfg.Layouts.AutoLaunchOnAdd, "auto_launch_on_add backfill stays")
	assert.Empty(t, cfg.Layouts.Presets, "migration must not seed presets")
```

and replace `assert.Contains(t, text, "[[layouts.presets]]")` with `assert.NotContains(t, text, "[[layouts.presets]]")`. Delete the now-unneeded `name`/`arrange`/`panes` `Contains` assertions and the `Name`/`Arrange`/`Panes` `NotContains` trio (they existed to check preset key casing in the seeded TOML).

`TestInitIsGlobalOnly` (line 1510): replace the final assertion with:

```go
	assert.Empty(t, cfg.Layouts.Default, "Init must not merge cwd .kwt.toml")
```

(The cwd `.kwt.toml` sets `default = "from-cwd"`; an empty result still proves no merge.)

Add a new test after `TestInitBackfillsWorkspaceConfigIntoExistingConfig`:

```go
func TestInitLeavesExistingLayoutsUntouched(t *testing.T) {
	kwtHome := t.TempDir()
	t.Setenv("KWT_HOME", kwtHome)
	require.NoError(t, os.WriteFile(filepath.Join(kwtHome, "config.toml"), []byte(`
[layouts]
default = "quad"
auto_launch_on_add = false

[[layouts.presets]]
name = "quad"
arrange = "tiled"
panes = [""]
`), 0o600))

	viper.Reset()
	t.Cleanup(viper.Reset)
	require.NoError(t, Init())

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "quad", cfg.Layouts.Default)
	assert.False(t, cfg.Layouts.AutoLaunchOnAdd)
	require.Len(t, cfg.Layouts.Presets, 1)
	assert.Equal(t, "quad", cfg.Layouts.Presets[0].Name)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestDefaultLayoutsConfig|TestInitBackfills|TestInitIsGlobalOnly|TestInitLeavesExistingLayouts' -v`
Expected: FAIL — migration still writes `quad` and seeds presets.

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

1. Delete `defaultLayouts()` (lines 37-49) — its only caller is the migration block being rewritten.
2. Replace the layouts migration block (lines 287-301, from `var layouts models.LayoutsConfig` through the presets `if`) with:

```go
	if !globalViper.IsSet("layouts.auto_launch_on_add") {
		globalViper.Set("layouts.auto_launch_on_add", true)
		migrated = true
	}
```

Remove the `go.kenn.io/kwt/pkg/models` import if this was its last use in the file (run `goimports -w internal/config/config.go`).

3. In `defaultConfigTOML()`, replace the `[layouts]` header block:

```toml
[layouts]
# Unset (or "none") launches a blank single-pane session.
# Set to a preset name below to launch that layout by default.
# default = "quad"
auto_launch_on_add = true
```

The four `[[layouts.presets]]` entries stay unchanged.

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/config/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "Stop writing a default layout; respect existing configs"
```

---

### Task 5: Completion and flag help text

**Files:**
- Modify: `internal/cmd/completion.go` (~line 122)
- Modify: `internal/cmd/add.go` (~line 66)
- Modify: `internal/cmd/open.go` (~line 43)

**Interfaces:** none — string changes only.

- [ ] **Step 1: Update the strings**

`internal/cmd/completion.go` line 122:

```go
		{"layouts.default", "Default workspace layout preset; unset or 'none' means a blank session"},
```

`internal/cmd/add.go` line 66 and `internal/cmd/open.go` line 43 — same help string in both:

```go
	addCmd.Flags().StringVar(&addLayout, "layout", "", "Workspace layout preset to launch (\"none\" = blank session)")
```

```go
	openCmd.Flags().StringVar(&openLayout, "layout", "", "Workspace layout preset to launch (\"none\" = blank session)")
```

- [ ] **Step 2: Build and spot-check help output**

Run: `make build && ./kwt add --help | grep -A1 layout && ./kwt open --help | grep -A1 layout`
Expected: both show `("none" = blank session)`.

- [ ] **Step 3: Run cmd package tests**

Run: `go test ./internal/cmd/... `
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cmd/completion.go internal/cmd/add.go internal/cmd/open.go
git commit -m "Document blank-session default in help and completion text"
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md` (config prose ~lines 78-81, `[layouts]` example ~lines 100-113)
- Modify: `docs/reference/configuration.md` (`[layouts]` example ~lines 27-39 and following prose)
- Modify: `docs/index.md` (line 33)
- Modify: `docs/workflows/agent-workspaces.md` (Layouts section, lines 6-23)
- Modify: `docs/workflows/directory-workspaces.md` (Sessions and layouts section, ~lines 34-48)

**Interfaces:** none.

- [ ] **Step 1: README.md**

Replace the paragraph "``config.toml`` is the source of truth for layouts and agent commands. When kwt creates the file for the first time, it writes a starter set of agents and layouts. After that, kwt does not rely on hidden layout or agent defaults in the binary." with:

```markdown
`config.toml` is the source of truth for layouts and agent commands. Workspaces
launch as a blank single-pane session unless a layout is selected — via
`--layout`, `--select-layout`, the TUI `L` key, or `layouts.default`. The name
`none` is reserved and always means a blank session. When kwt creates the file
for the first time, it writes a starter set of agents and layout presets to opt
into; after that, kwt does not rely on hidden layout or agent defaults in the
binary.
```

In the README config example, replace the `[layouts]` header lines:

```toml
[layouts]
# default = "quad"  # unset or "none" = blank single-pane session
auto_launch_on_add = true
```

(the `[[layouts.presets]]` entries stay).

- [ ] **Step 2: docs/reference/configuration.md**

Make the same `[layouts]` example change as the README. After the example's "Pane entries are shell commands…" paragraph, add:

```markdown
`layouts.default` is optional. When it is unset — or set to the reserved name
`none` — workspaces launch as a blank single-pane session in the worktree
directory. Repository-local `.kwt.toml` files may also set
`layouts.default = "none"` to opt a single project back into blank sessions
when the global config names a preset.
```

- [ ] **Step 3: docs/index.md**

Replace line 33 (`- Explicit tmux layouts for coding agents, shells, and review tools.`) with:

```markdown
- Blank tmux sessions by default, with opt-in layouts for coding agents,
  shells, and review tools.
```

- [ ] **Step 4: docs/workflows/agent-workspaces.md**

In the Layouts section, after "The empty string creates a plain shell…" paragraph, add:

```markdown
Layouts are opt-in: without `--layout`, `--select-layout`, or a
`layouts.default`, a workspace is a blank single-pane session. The reserved
name `none` selects the blank session explicitly.
```

- [ ] **Step 5: docs/workflows/directory-workspaces.md**

In "Sessions and layouts", replace "Layouts behave exactly as for worktrees, including the trust-gated per-directory default in the directory's `.kwt.toml`:" with:

```markdown
Layouts behave exactly as for worktrees: blank single-pane sessions by
default, with the same opt-ins, including the trust-gated per-directory
default in the directory's `.kwt.toml`:
```

- [ ] **Step 6: Commit**

```bash
git add README.md docs/reference/configuration.md docs/index.md docs/workflows/agent-workspaces.md docs/workflows/directory-workspaces.md
git commit -m "Document blank default session and opt-in layouts"
```

---

### Task 7: Full verification

**Files:** none new.

- [ ] **Step 1: Full test suite**

Run: `make test`
Expected: PASS across all packages.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: no issues. Fix anything reported (e.g. unused imports left by the config.go deletion), then re-run.

- [ ] **Step 3: End-to-end smoke test**

Run (uses an isolated config home so your real config is untouched):

```bash
make build
export KWT_HOME=$(mktemp -d)
./kwt config list | grep -A2 '\[layouts\]'
```

Expected: fresh config with no `default =` line under `[layouts]` (only the comment) and `auto_launch_on_add = true`.

If a tmux server is available, verify blank creation directly:

```bash
./kwt add -b smoke/blank-default --no-launch   # inside a test repo, or skip
```

At minimum verify construction via the unit layer (already covered by `TestBuildConstructionSequenceSinglePane` and `TestResolveLayoutBlankWhenNothingConfigured`).

- [ ] **Step 4: Commit any fixups**

```bash
git add -A && git commit -m "Fix lint findings for blank default session"
```

(skip if the tree is clean).
