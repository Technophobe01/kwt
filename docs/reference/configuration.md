# Configuration

Global config lives at `~/.config/kwt/config.toml`, or at
`$KWT_HOME/config.toml` when `KWT_HOME` is set. Repository-local overrides live
in `.kwt.toml` and are trust-gated before use.

The global file is the source of truth for worktree naming, tmux layouts, agent
commands, repository setup rules, and the known project registry.

```toml
[worktree]
basedir = "~/worktrees"
auto_mkdir = true

[naming]
template = "{{.FullPath}}/{{.Branch}}"

[naming.sanitize_chars]
"/" = "-"
":" = "-"

[agents]
codex = "codex"
claude = "claude"
roborev = "roborev tui"

[layouts]
default = "quad"
auto_launch_on_add = true

[[layouts.presets]]
name = "quad"
arrange = "even-horizontal"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]

[[layouts.presets]]
name = "stack"
arrange = "even-vertical"
panes = ["agent:codex", "agent:claude", "agent:roborev", ""]
```

Pane entries are shell commands. `agent:<name>` expands through the `[agents]`
table before tmux starts, so command flags live in one local config file.

## Project registry

The dashboard lists worktrees from the configured base directory, the current
launch repository, and registered projects. Running `kwt` inside a repository
registers or refreshes that repository so future dashboard launches can find its
worktrees from anywhere.

Project entries are discovery metadata, not worktree-creation policy:

```toml
[[projects]]
repository = "github.com/kenn-io/kwt"
name = "kwt"
path = "~/code/kwt"
last_touched = "2026-07-04T12:00:00Z"
```

## Repository setup

Optional repository settings can copy files or run commands when new worktrees
are created by `kwt add`:

```toml
[[repository_settings]]
repository = "~/code/myapp"
basedir = "./worktrees"
copy_files = ["templates/.env.example"]
setup_commands = [
  "npm install",
  'printf "branch=%s\npath=%s\n" "{{.Branch}}" "{{.Path}}" > .worktree-info',
]
```

Template variables include `Host`, `Owner`, `Repository`, `FullPath`, `Branch`,
`Hash`, and `Path`. Quote variables in shell commands when values may contain
spaces.

Remote-only multi-machine sync skips repository setup (`copy_files` and
`setup_commands`) because the branch name is reported by another host. Run any
project bootstrap command manually after syncing if that branch needs it.

## Multi-machine sync config

Multi-machine sync is an opt-in subsystem. The public command namespace is
`kwt sync`, and the config section is `[fleet]`.

```toml
[fleet]
enabled = true
host_id = "host-a"
hub_url = "https://host-a.example"
token_file = "~/.config/kwt/fleet.token"

[fleet.hub]
listen_addr = "127.0.0.1:8787"
store_path = "~/.local/share/kwt/fleet/state.json"
```

Plain `http://` hub URLs are accepted only for loopback hosts. Use HTTPS for a
multi-machine hub URL, commonly by serving the loopback hub through a private TLS
endpoint.

See [Multi-machine sync](../multi-machine-sync.md) for the user-facing workflow
and [Multi-machine sync architecture](../design/multi-machine-sync.md) for the
wire protocol and hub behavior.
