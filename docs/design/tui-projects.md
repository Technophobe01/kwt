# TUI and Project Registry

The dashboard is the primary `kwt` surface. It should let a user steer worktrees
across repositories without first changing directories.

## Cross-project model

`kwt` keeps a global project registry in the config file under
`~/.config/kwt/config.toml`, or `$KWT_HOME/config.toml` when `KWT_HOME` is set.
The registry records repositories `kwt` has seen:

- stable repository identity, such as `github.com/kenn-io/kwt`;
- display name;
- local repository root path for this host;
- last touched timestamp.

This registry is discovery metadata only. It is deliberately separate from
`repository_settings`, because repository settings change how worktrees are
created.

Dashboard discovery merges three sources:

- worktrees under `worktree.basedir`;
- worktrees reported by every registered project;
- the repository from which the dashboard was launched.

Duplicate paths are de-duplicated. Missing or stale registered paths are ignored
so removing a local clone does not break the dashboard.

## Project filters and perspectives

Lowercase `p` is a project-name filter. It narrows visible rows, but it is still
a filter.

Uppercase `P` selects a project perspective. The active perspective is either
`all` or one project. It filters rows and also becomes the project context for
actions such as `n` new worktree. That distinction lets a user launch `kwt`,
press `P`, choose a project, press `n`, and create the worktree in that project
without exiting the TUI.

Filtering order is:

1. active project perspective;
2. lowercase project-name filter;
3. text search with `/`.

If the active project has no rows after refresh, the dashboard should keep the
perspective and render an empty state instead of silently changing context.

## Footer hints

The footer uses compact key/description cells and wraps to the terminal width.
The model reserves the rendered footer height so wrapped hints do not push the
status line off-screen.

## Testing expectations

TUI tests should assert user-visible behavior: rendered rows, active
perspective, selection/cancel behavior, handoff intent, and command outcomes.
They should not assert that helper functions are wired exactly as they happen to
be written.
