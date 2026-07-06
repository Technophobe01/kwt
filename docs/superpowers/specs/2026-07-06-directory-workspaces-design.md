# Directory Workspaces

**Date:** 2026-07-06
**Status:** Approved
**Base branch:** `feature/fleet-sync` (depends on the fleet-era TUI row model)

## Problem

kwt can only create and surface tmux workspaces bound to a git worktree. There
is no way to register a plain directory (notes, scratch space, a non-git
project) as a workspace, open it with a layout, and see it in the dashboard.

## Decision summary

Directory workspaces become a first-class registry entry and a third TUI row
flavor. They are anchored to a directory, persist in global config when their
session is not running, and stay local-only (never published to the sync hub).

Rejected alternatives: modeling directories as fake projects/worktrees (plants
non-git lies in a git-centric data model that fleet identity logic depends
on), and live-session discovery without a registry (rows vanish when sessions
die; nothing to "open").

## Data model and config

- New global-config section:

  ```toml
  [[workspaces]]
  name = "notes"
  path = "/Users/me/notes"
  ```

- `models.Workspace{Name, Path string}`; `models.Config.Workspaces []Workspace`
  (`mapstructure:"workspaces"`). Paths are expanded and symlink-resolved on
  load like project paths.
- `config.RegisterWorkspace(workspace models.Workspace) error`, mirroring
  `RegisterProject`:
  - Expand and resolve `Path`; error if it does not exist or is not a
    directory (fail fast with the offending path in the message).
  - Default `Name` to `filepath.Base(path)` when empty.
  - Dedupe by resolved path: re-registering an existing path updates its name.
  - Reject a duplicate name registered for a different path; the error
    suggests `--name`.
- `mergeLocalConfig` skips the `workspaces` key, same as `projects` and
  `fleet.*`: the registry is machine-level and repo-local `.kwt.toml` must not
  alter it.
- Fleet manifests do not include workspaces. No hub, client, or state changes.

## CLI

New `kwt workspace` command group:

- `kwt workspace add [path] [--name X]` — path defaults to the current
  directory. Registers only; does not open a session.
- `kwt workspace list` — table of name, path, and session state
  (live/stopped).
- `kwt workspace remove <name>` — unregisters. Never touches the directory.
  A live session is left running and the command says so. Unknown names error
  and list the registered names.

Auto-register on open: when the TUI launches from a directory that is not
inside a git repository, register it as a workspace best-effort (silently,
mirroring launch-repo project registration), except when the directory is the
user's home directory, which is never auto-registered.

## TUI

- `dashboard.Row` gains a third flavor: `Workspace *WorkspaceInfo` with
  `Name`, `Path`, plus the existing `SessionName`/`SessionLive` fields on the
  row. `Workspace` is mutually exclusive with `Entry` and `Fleet` (which can
  appear together on merged worktree rows).
- The backend appends one row per registered workspace after worktree and
  fleet rows, computing session liveness from the same `listSessions` map.
- Rendering: workspace name in the project column, tilde-abbreviated path in
  the ref column, `local` in the machines column, session liveness in the
  status column.
- Actions on workspace rows:
  - Open in tmux, attach outside tmux, kill session, and layout selection
    work exactly as for worktree rows.
  - Git-specific actions (new branch, sync/materialize, worktree remove) are
    gated off with a status-line message.
  - The remove keybinding unregisters the workspace after the standard
    confirm prompt.
- Name and path join the filter/search haystack.

## Sessions and layouts

- Session name: `kwt-workspace-dir-{name}-{hash8(path)}` via a new
  `tmux.DirWorkspaceSessionName(name, path string) string`. The hash is over
  the resolved path (collision resistance); the name passes through the
  existing tmux sanitizer. The result still matches the existing
  `kwt-workspace-*` session-parsing regex.
- Opening uses the existing `tmux.WorkspaceRunner.EnsureAndAttach` with the
  workspace directory as the pane root and the same layout presets.
- Per-directory layout defaults reuse the trust-gated
  `config.LoadRepoLayoutDefault(dir, interactive)` mechanism unchanged.

## Error handling

- `add`: missing/non-directory path, and duplicate-name errors as above.
- `remove`: unknown name lists registered names.
- TUI: gated actions produce a short status-line message, not an error state.
- Auto-registration failures are silent (best-effort), matching project
  auto-registration.

## Testing

- Config: register/dedupe by path, name-collision error, missing-path error,
  name defaulting, `workspaces` excluded from local-config merge.
- tmux: `DirWorkspaceSessionName` determinism, sanitization, regex
  compatibility.
- Backend: `List` returns workspace rows with correct liveness; home-dir
  guard on auto-registration; non-git launch dir auto-registers.
- TUI: rendering of workspace rows, action gating messages, unregister flow
  with confirmation.
- CLI: `add`/`list`/`remove` happy paths and each error path.
