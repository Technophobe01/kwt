# Quickstart

## Install

```sh
go install go.kenn.io/kwt/cmd/kwt@latest
```

From a clone:

```sh
git clone https://github.com/kenn-io/kwt.git
cd kwt
go build -o kwt ./cmd/kwt
```

## Open the dashboard

From any directory:

```sh
kwt
```

Bare `kwt` opens the full-screen dashboard when stdin and stdout are interactive.
Use `kwt tui` when you want the explicit command form. Launching from a Git
repository registers it as a project; launching from a plain directory registers
it as a [directory workspace](../workflows/directory-workspaces.md) and
pre-selects its row, so `enter` opens a tmux session right there.

Useful keys:

| Key     | Action                                                  |
| ------- | ------------------------------------------------------- |
| `enter` | Attach to the selected workspace.                       |
| `n`     | Create a worktree in the active project perspective.    |
| `P`     | Switch the active project perspective.                  |
| `p`     | Filter visible projects by name.                        |
| `/`     | Search rows within the active perspective/filter.       |
| `L`     | Select a workspace layout.                              |
| `d`     | Delete the selected worktree or unregister a workspace. |
| `K`     | Kill the selected live tmux workspace.                  |
| `s`     | Sync a remote-only branch row locally.                  |
| `c`     | Open a shell in the selected worktree.                  |
| `r`     | Refresh.                                                |
| `?`     | Toggle help.                                            |

## Create a worktree

```sh
kwt add -b feature/new-ui
```

By default, `kwt add` creates the worktree and launches a tmux workspace — a
blank single-pane session unless a [layout](../reference/configuration.md) is
selected. To create without launching:

```sh
kwt add --no-launch -b feature/new-ui
```

## Use worktrees from scripts

```sh
cd "$(kwt get feature/new-ui)"
kwt exec feature/new-ui -- npm test
```

## Clean up

```sh
kwt remove feature/new-ui
kwt remove -b feature/new-ui
kwt prune
```

`remove -b` removes both the worktree and the matching branch. `prune` cleans up
stale Git worktree metadata.
