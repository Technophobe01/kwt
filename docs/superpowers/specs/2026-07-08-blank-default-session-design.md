# Blank tmux session by default; layouts become opt-in

**Date:** 2026-07-08
**Status:** Approved for planning

## Problem

Today every workspace launch (`kwt add` auto-launch, `kwt open`, TUI attach)
must resolve to a layout preset. `ValidateLayouts` hard-errors when zero
presets are configured, the generated config ships `layouts.default = "quad"`
(three agent panes plus a shell), and config migration force-writes that
default and the preset library into any config missing them. A plain tmux
session — one pane, login shell, cwd at the worktree — is not expressible.

That inverts the natural default. A blank session should be the zero-config
baseline; multi-pane agent layouts should be an opt-in add-on.

## Design

### Concept

A workspace launch no longer requires a layout. When no layout is selected by
flag, picker, repo default, or global default, the workspace is a blank
session: one pane, plain login shell, cwd at the worktree root. The layout
system itself (presets, agents, resolution precedence) is unchanged — it just
stops being mandatory.

The reserved name `none` means "blank session" anywhere a layout name is
accepted: `--layout none`, `layouts.default = "none"` (global or repo
`.kwt.toml`), the `--select-layout` picker, and the TUI layout cycler.

### Representation

Blank is a degenerate layout, not a new code path:

```go
models.Layout{Name: "none", Panes: []string{""}}
```

It flows through `ResolvePaneCommands`, `EnsureAndAttach`, session naming, and
the workspace runner unchanged. `BuildConstructionSequence` skips the
`select-layout` call when the layout has fewer than two panes (correct for any
single-pane layout, not just blank).

### Resolution (`internal/tmux/layout.go`)

- `ResolveLayout` keeps its precedence chain (`--layout` → `--select-layout` →
  repo default → global default). When the resolved name is `""` or `"none"`,
  it returns the blank layout instead of erroring.
- `ValidateLayouts` no longer errors on zero presets. It still validates
  arrange names, non-empty panes, and agent references for presets that exist.
  New error: a preset named `none` (reserved). Unchanged error: a non-empty
  `layouts.default` naming a nonexistent preset — except `"none"`, now valid.
- The `--select-layout` picker list gets a `none` entry prepended.

### TUI (`internal/cmd/tui_backend.go`, `internal/tui/model.go`)

`LayoutNames()` prepends `"none"` to the configured preset names. The layout
cycle becomes `"" (default) → none → <presets…> → ""`, so blank is reachable
for a single launch even when a global default is configured. The TUI passes
the selected name to `ResolveLayout` as the layout flag, so reserved-name
resolution covers it with no other TUI changes. Because the list is never
empty, `cycleLayout`'s "no layouts configured" branch is dead and is deleted.

### Config (`internal/config/config.go`)

- `defaultLayouts()` and the generated `config.toml` template keep
  `auto_launch_on_add = true` and the four presets (quad, grid, focus, stack)
  as an opt-in library, but ship no `layouts.default` — blank is implicit.
- Migration stops force-writing `layouts.default` and stops re-seeding presets
  into existing configs. Config files are respected as-is.

**Deliberate breakage:** a hand-authored config with `layouts.default =
"quad"` but no presets becomes a hard error ("layouts.default \"quad\" is not
a defined preset"). This is intentional, not an oversight: configs the old
migration touched already have the presets materialized on disk, the error
message is actionable, and conditional backfill of built-ins would be
compatibility magic this project avoids.

### Untouched

`kwt add` launch decision (`auto_launch_on_add`, `--no-launch`), session
naming, attach/ensure logic, directory workspaces, `[agents]` config. `add`,
`open`, and the TUI all go through `ResolveLayout`/`ValidateLayouts` and pick
up the behavior with no per-command launch changes.

## Error handling

- `--layout none` → blank session.
- `--layout <unknown>` → existing error listing available presets.
- Preset named `none` → config validation error.
- `layouts.default` naming a missing preset → existing validation error
  (now also covers the default-only-config breakage above).

## Testing

- Blank resolution: no flags, no defaults → blank layout; `"none"` via flag,
  repo default, and global default → blank.
- Reserved name: preset named `none` rejected by `ValidateLayouts`.
- Zero presets: `ValidateLayouts` passes; launch produces a blank session;
  `--layout quad` errors.
- Construction: single-pane layout emits no `select-layout` call.
- Migration: existing config with `default = "quad"` and presets is left
  untouched; config missing `layouts.default` is not backfilled.
- TUI: `LayoutNames()` starts with `none`; cycling reaches `none` and wraps.
- Update existing layout/config/add/open/tui tests that assume a mandatory
  default or preset library.

## Documentation

- Docs-site pages describing layouts and the default behavior.
- README config examples (`[layouts]` block, `layouts.default` mentions).
- Generated `config.toml` template comments.
- `completion.go` config-key description: `layouts.default` → "Default
  workspace layout preset; unset or 'none' means a blank session".
- `--layout` / `--select-layout` flag help text in `add` and `open`.
