# kwt Project Registry and TUI Project Filter Design

## Goal

Make the TUI show worktrees for every project kwt knows about, not only the configured worktree base directory or the repository where the TUI was launched.

## Design

kwt's global state should live in `$KWT_HOME/config.toml` when `KWT_HOME` is set, with the existing `~/.config/kwt/config.toml` location retained as the fallback. The global config gains a `projects` registry that records repositories kwt has touched. This registry is discovery metadata only; it must not reuse `repository_settings`, because those entries alter worktree creation behavior.

Each project entry stores a stable repository identity, display name, repository root path, and last-touched timestamp. The TUI backend discovers worktrees by merging the configured `worktree.basedir`, every registered project's `git worktree list`, and the current launch repository. Duplicate paths are de-duplicated.

The TUI adds `p` as a project filter prompt. The project filter narrows rows by repository/project name. The existing `/` text filter continues to search within the active project filter. An empty project filter means all projects.

## Error Handling

Missing or stale registered project paths are ignored during TUI discovery so a removed repository does not break the dashboard. Registering a project should be best-effort in read-oriented TUI flows and should not prevent the dashboard from opening.

## Testing

Tests should cover `$KWT_HOME` config resolution, config round-tripping for registered projects, backend discovery from registered projects, automatic launch repo registration, and TUI project filter behavior.
