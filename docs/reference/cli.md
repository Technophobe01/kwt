# CLI Reference

Run `kwt <command> --help` for command-specific flags. This page summarizes the
stable command surface.

| Command          | Purpose                                                |
| ---------------- | ------------------------------------------------------ |
| `kwt`, `kwt tui` | Open the cross-project dashboard.                      |
| `kwt add`        | Create a worktree and optionally launch its workspace. |
| `kwt open`       | Fuzzy-pick and attach to a workspace.                  |
| `kwt list`       | List worktrees.                                        |
| `kwt status`     | Show Git status, sync state, and activity.             |
| `kwt get`        | Print a matching worktree path.                        |
| `kwt cd`         | Open a shell in a matching worktree.                   |
| `kwt exec`       | Run a command in a matching worktree.                  |
| `kwt remove`     | Delete a worktree, optionally its branch.              |
| `kwt prune`      | Clean up stale Git worktree metadata.                  |
| `kwt tmux`       | Manage standalone tmux sessions.                       |
| `kwt config`     | Read and write config values.                          |
| `kwt completion` | Generate shell completion and integration.             |

## Examples

```sh
kwt add -b fix/parser-race
kwt open parser
kwt status
kwt exec fix/parser-race -- go test ./internal/parser
kwt config get layouts.default
kwt config set --local layouts.default stack
```

## Exit behavior

Commands intended to launch the dashboard or attach to tmux require an
interactive terminal. In non-interactive contexts, use data-oriented commands
such as `list`, `status`, `get`, and `exec`.
