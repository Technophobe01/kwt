---
title: kwt
description: A Git worktree manager for terminal-native, tmux-backed agentic engineering.
---

# kwt

`kwt` is a focused Git worktree manager for people who live in terminals, keep
many repositories active, and use tmux-backed agent workspaces.

It gives you a cross-project dashboard, predictable worktree paths, configurable
tmux layouts, and small CLI commands that compose well in shell scripts. The
default loop is intentionally direct: pick a branch, enter a workspace, and keep
the code, shell, and agents close together.

## Start here

```sh
go install go.kenn.io/kwt/cmd/kwt@latest
kwt
```

Run `kwt` from a repository to open the dashboard. The dashboard registers that
repository, shows worktrees from known projects, and attaches to a tmux workspace
for the selected branch. Plain directories work too: launching from a non-Git
directory registers it as a directory workspace with its own tmux session.

## What kwt optimizes for

- Fast terminal workflows over browser dashboards.
- Multiple active repositories without changing directories first.
- Worktrees that are easy for humans and agents to locate.
- Blank tmux sessions by default, with opt-in layouts for coding agents,
  shells, and review tools.
- Local-first behavior by default. Optional multi-machine sync is advisory, not
  a hidden file synchronizer.

## Common commands

```sh
kwt add -b feature/machine-view
kwt open
kwt status
kwt exec feature/machine-view -- go test ./...
kwt remove feature/machine-view
```

See the [quickstart](get-started/quickstart.md), [CLI reference](reference/cli.md),
and [configuration reference](reference/configuration.md) for the maintained
surface. [Directory workspaces](workflows/directory-workspaces.md) covers tmux
sessions for non-Git directories, [multi-machine sync](multi-machine-sync.md)
covers trusted multi-host worktree visibility, and the
[design notes](design/index.md) preserve the decisions behind the cross-project
TUI and synchronization architecture.
