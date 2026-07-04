# Multi-machine Sync

Multi-machine sync gives `kwt` a shared view of active worktrees across the
trusted machines you use for development. It answers practical questions:

- Which branches did I materialize on the desktop?
- Is this worktree missing on the laptop?
- Did another host observe a different head for the same branch?
- Which machine last saw dirty or untracked files?

It is advisory. `kwt` publishes worktree manifests and renders the combined
state, but it does not sync file contents, clone repositories, create worktrees,
delete worktrees, or lock branches on other hosts.

## How it works

One machine runs the hub. Every enabled machine, including the hub machine,
publishes a local manifest. The hub stores the latest manifest for each host and
serves a grouped multi-machine view.

The public command namespace is `kwt fleet` for now:

```sh
kwt fleet serve
kwt fleet publish
kwt fleet status
kwt fleet forget <host-id>
```

`kwt fleet status` publishes this host best-effort before reading the hub. Worktree
mutation commands also publish after successful local changes when multi-machine
sync is enabled.

## Configure it

Multi-machine sync is disabled by default. Enable it explicitly:

```toml
[fleet]
enabled = true
host_id = "host-a"
hub_url = "http://host-a.example:8787"
token_file = "~/.config/kwt/fleet.token"

[fleet.hub]
listen_addr = "100.x.y.z:8787"
store_path = "~/.local/share/kwt/fleet/state.json"
```

Use `token_file` or `token_env`; do not put the token inline in `config.toml`.
The intended deployment is over Tailscale or an equivalent trusted private
network. The hub rejects public and unspecified listen addresses by default.

## Freshness and differences

Each host reports the Git state it can observe locally: branch or detached head,
head commit, local ahead/behind relative to that host's remote-tracking refs,
dirty counts, path, and last activity.

The multi-machine view deliberately avoids claiming ancestry it cannot prove. If
two hosts report different heads for the same branch, `kwt` reports that they
differ; it does not say one host is behind another unless the local Git clone has
the commits needed to prove that relationship.

## Retired hosts

The hub keeps the latest manifest per host until you remove it:

```sh
kwt fleet forget old-host
```

Use this when a machine is retired or renamed so old observations stop appearing
in the multi-machine view.
