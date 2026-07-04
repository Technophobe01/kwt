# kwt Fleet Sync Design

## Goal

Make kwt optionally coordinate active Git worktrees across a small fleet of trusted machines. The first version should show the union of fleet worktrees and identify which worktrees are missing or different on the current host. It should not synchronize uncommitted file contents, delete worktrees, or require background daemons for single-machine users.

Fleet sync is opt-in. When disabled, kwt must not start daemons, open listeners, call a hub, or add fleet-specific rows to the TUI or status output.

## Scope

The v1 scope is advisory Git-backed state:

- Discover local projects and worktrees.
- Publish a local manifest to a configured hub.
- Store latest manifests by host on the hub.
- Read the fleet union from the hub.
- Show whether each fleet worktree is materialized on the current host.
- Show when the current host's copy differs from another observed host copy.

Out of scope for v1:

- Syncing dirty, staged, untracked, ignored, or generated file contents.
- Automatically cloning repos or creating/removing worktrees.
- Electing a hub or failing over to another hub.
- Requiring a spoke daemon on every machine.
- Using Tailscale node identity as the application auth mechanism.

## Architecture

Fleet sync uses a hub-and-spoke model over a trusted private network, normally Tailscale. One kwt installation is configured with a hub listener. Every fleet-enabled node, including the hub machine, publishes its local manifest. The hub is intentionally a dumb store: it authenticates the request, validates the manifest, stores the latest manifest per host, and serves a fleet-state union.

The hub must not mutate any spoke filesystem. Each node compares the hub state with its own local state and decides what advisory status to show. Future versions can add host-local materialization policy, but the default behavior remains advisory.

The hub daemon uses `go.kenn.io/kit/daemon` for daemon lifecycle, endpoint parsing, runtime records, liveness probes, and listen locking. Application API routes, auth, manifest storage, and fleet reconciliation live in kwt.

Spokes do not need a long-running daemon in v1. A fleet-enabled CLI can publish on worktree mutations such as `kwt add`, `kwt remove`, and `kwt prune`, and can also publish explicitly through `kwt fleet publish`. Fleet read commands always attempt a best-effort publish before reading hub state, then continue even if that publish fails. A spoke daemon can be added later for users who want periodic updates when worktrees are changed outside kwt.

Publish-on-mutation and publish-before-read calls must use a short timeout, 2 seconds by default, and must not retry on the mutation path. Fleet metadata freshness must not make local worktree operations feel slow or fail when the hub is offline.

Plain HTTP is acceptable for v1 because the expected deployment is over Tailscale or an equivalent encrypted private network. That assumption is load-bearing: hub listen addresses should reject public and unspecified binds by default. The default listen policy should use `go.kenn.io/kit/daemon.RequireNonPublic`, which allows loopback, private, link-local, and Tailscale CGNAT addresses such as `100.64.0.0/10`.

## Configuration

Fleet config is disabled by default:

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

There is no `role` field. A node with `[fleet.hub]` configured is the hub. The hub machine's CLI still publishes through the same HTTP API as any other node; only the hub daemon reads and writes the hub store file. A hub node should set `hub_url` to its own listener URL, or kwt should default an empty `hub_url` from `[fleet.hub].listen_addr`. A node without `[fleet.hub]` is a normal publisher/client.

If `host_id` is omitted, kwt defaults it from `os.Hostname()`. Host IDs are normalized by trimming whitespace and lowercasing ASCII letters, then validated against `^[a-z0-9._-]+$`. If the normalized host ID is empty or contains any other character, fleet commands must fail before publishing rather than writing under an unsafe or empty host key. Users can set `host_id` explicitly when hostnames are unstable, not unique enough, or do not normalize to the allowed format.

The hub accepts clients that present the configured fleet token in `Authorization: Bearer <token>`. Tailscale reachability is the network boundary, not the identity system. Tokens must not be written inline in `config.toml`; v1 must support both `token_file` and `token_env`.

The v1 behavior is advisory only. kwt reports differences but makes no filesystem changes. Future modes such as `materialize` can be added as host-local policy when there is a second real behavior to configure.

## CLI Surface

The v1 command surface is:

- `kwt fleet serve`: run the hub HTTP server in the foreground. Service managers or later daemon autostart wrappers use this command as the server process.
- `kwt fleet publish`: build the local manifest and publish it to `hub_url`.
- `kwt fleet status`: publish first with the short best-effort timeout, fetch the fleet union, and render the Fleet View table.
- `kwt fleet forget <host_id>`: ask the hub to delete a retired host.

Existing commands that mutate worktrees should trigger the same best-effort publisher after a successful local mutation when fleet is enabled. Existing TUI/status integration can consume the same fleet-state client after the core API and reconciliation model exist, but `kwt fleet status` is the required v1 rendering surface.

## Project Identity

Fleet rows must be keyed by logical project identity, not local filesystem paths. The existing `models.Project.Repository` field is the closest current model and should remain the stable key when it is a normalized repository identity such as `github.com/kenn-io/kwt`.

The same logical project can have different local roots on different hosts:

```toml
[[projects]]
repository = "github.com/kenn-io/kwt"
name = "kwt"
path = "~/code/kwt"
```

Different hosts can use different `path` values for the same `repository`. Registering or refreshing a project should match first by stable repository identity when present, and only use local path as a same-host fallback.

Remote URL normalization must be explicit and table-tested. `git@github.com:kenn-io/kwt.git`, `https://github.com/kenn-io/kwt`, and `https://github.com/kenn-io/kwt.git` should normalize to `github.com/kenn-io/kwt`. Owner/name differences remain distinct projects, so a fork under a different owner is a different fleet project unless the user explicitly configures the canonical identity.

SSH host aliases are not safely inferable from Git URLs alone. v1 should allow explicit config to set or override the project identity when automatic normalization would split the fleet incorrectly.

## Manifest Schema

Each publish sends a versioned manifest:

```json
{
  "schema_version": 1,
  "host_id": "host-a",
  "host": {
    "hostname": "Host-A",
    "platform": "darwin/arm64"
  },
  "observed_at": "2026-07-04T12:00:00Z",
  "projects": [
    {
      "identity": "github.com/kenn-io/kwt",
      "name": "kwt",
      "local_root": "/home/user-a/code/kwt",
      "remote_url": "git@github.com:kenn-io/kwt.git"
    }
  ],
  "worktrees": [
    {
      "project_identity": "github.com/kenn-io/kwt",
      "kind": "branch",
      "ref": "feature/fleet-sync",
      "branch": "feature/fleet-sync",
      "path": "/home/user-a/worktrees/github.com/kenn-io/kwt/feature-fleet-sync",
      "head": "abcdef123456",
      "head_time": "2026-07-04T11:30:00Z",
      "upstream": "origin/feature/fleet-sync",
      "ahead": 1,
      "behind": 0,
      "status": {
        "modified": 0,
        "added": 0,
        "deleted": 0,
        "untracked": 0,
        "staged": 0,
        "conflicts": 0
      },
      "last_activity": "2026-07-04T11:45:00Z",
      "is_main": false
    }
  ]
}
```

`schema_version` lets hub and clients fail clearly during independent upgrades. The hub should reject unknown required schema versions with a clear error instead of storing a manifest it may misinterpret.

Worktree identity uses `(project_identity, kind, ref)`. The manifest must include `ref` directly so branch and detached worktrees can be keyed without inference:

- Branch worktrees use `kind = "branch"` and `ref = branch`.
- Detached HEAD worktrees use `kind = "detached"` and `ref = head`.

This prevents detached worktrees with empty branch names from collapsing into a single fleet row.

`ahead` and `behind` remain useful local status fields, but the fleet UI must not claim cross-host ancestry from them. They are relative to each host's local remote-tracking refs, which may be stale. When two hosts report different heads for the same branch, kwt should say "differs from host X, observed Y ago" rather than "behind host X" unless the local host has enough Git data to prove ancestry.

## Hub API

The v1 API is intentionally small:

- `GET /api/v1/ping` returns service, version, pid, and basic health.
- `POST /api/v1/fleet/hosts/{host_id}/manifest` stores a host manifest after token auth and schema validation.
- `GET /api/v1/fleet/state` returns the server-computed fleet union.
- `DELETE /api/v1/fleet/hosts/{host_id}` removes a retired host from the hub store.

Manifest requests should be capped at 1 MiB in v1. A larger fleet can raise that deliberately later, but an unbounded JSON POST should never be the default.

`GET /api/v1/fleet/state` should include an ETag. Clients can use `If-None-Match` so idle fleets poll cheaply. The response `state_version` is the canonical state hash, based on the stored manifests and warnings rather than request time. The HTTP ETag should be the quoted `state_version` value.

The hub should reject when a publish path host ID does not match the manifest host ID. It should also surface possible host ID collisions when the same `host_id` reports incompatible host characteristics, such as a different hostname/platform pair.

The state response is grouped by fleet row so clients do not have to reinterpret raw host manifests differently:

```json
{
  "schema_version": 1,
  "state_version": "sha256:abc123",
  "hosts": [
    {
      "host_id": "host-a",
      "hostname": "Host-A",
      "platform": "darwin/arm64",
      "observed_at": "2026-07-04T12:00:00Z"
    }
  ],
  "rows": [
    {
      "project_identity": "github.com/kenn-io/kwt",
      "project_name": "kwt",
      "kind": "branch",
      "ref": "feature/fleet-sync",
      "branch": "feature/fleet-sync",
      "observations": [
        {
          "host_id": "host-a",
          "path": "/home/user-a/worktrees/github.com/kenn-io/kwt/feature-fleet-sync",
          "head": "abcdef123456",
          "head_time": "2026-07-04T11:30:00Z",
          "upstream": "origin/feature/fleet-sync",
          "ahead": 1,
          "behind": 0,
          "status": {
            "modified": 0,
            "added": 0,
            "deleted": 0,
            "untracked": 0,
            "staged": 0,
            "conflicts": 0
          },
          "last_activity": "2026-07-04T11:45:00Z",
          "observed_at": "2026-07-04T12:00:00Z",
          "is_main": false
        }
      ]
    }
  ],
  "warnings": [
    {
      "code": "host_id_collision",
      "host_id": "host-a",
      "message": "host_id reported incompatible host characteristics"
    }
  ]
}
```

The hub owns grouping raw manifests into `rows` by `(project_identity, kind, ref)`. Clients own current-host interpretation, such as whether a row is materialized here or differs from another host observation.

## Fleet View

The fleet union groups reports by logical project and worktree identity. For each row, kwt can show:

- Project identity and display name.
- Branch or detached head.
- Hosts where the worktree is materialized.
- Whether the current host has the worktree.
- Whether the current host's head differs from the newest observed matching row.
- Whether any observed copy has uncommitted changes.
- Host freshness, such as "last seen 30s ago" or "last seen 30d ago".

Missing worktrees are advisory rows. They show that another host has materialized a worktree that this host has not. v1 should not automatically clone a repository or create a worktree for these rows.

Host report freshness is derived by clients from `observed_at`. A host last seen 30 days ago should not make the fleet look current, but v1 does not need a hub-side stale threshold or response field. The hub can later add a prune command or retention policy, but v1 needs a manual delete endpoint or command for retired hosts.

## Error Handling

When fleet is disabled, fleet errors cannot affect existing commands.

When fleet is enabled but the hub is unreachable, local commands should continue with local state and report a concise fleet warning. Mutation commands such as `kwt add` should not fail solely because manifest publication failed.

The hub should reject:

- Missing or invalid bearer auth token.
- Unknown manifest schema versions.
- Mismatched path and body host IDs.
- Invalid host ID format.
- Invalid or empty project identities.
- Manifest request bodies larger than 1 MiB.
- Public or unspecified listen addresses. v1 should not include a public-listen override.

Project paths that do not exist on a host should not poison fleet state. The local manifest builder should report only projects and worktrees it can observe.

## Testing

Tests should cover:

- Fleet config defaults keep the subsystem disabled and inert.
- Host ID defaults from hostname, normalizes to the allowed format, and fails before publish when the resulting ID is empty or invalid.
- Token loading from `token_file` and `token_env`, with no inline token requirement, and bearer-token auth on hub requests.
- Hub endpoint validation rejects public and unspecified listen addresses while allowing loopback and Tailscale CGNAT addresses.
- Remote URL normalization across SSH, HTTPS, trailing `.git`, and explicit identity override cases.
- Manifest schema validation, including unknown schema versions, detached HEAD worktree keys, and request bodies larger than 1 MiB.
- Hub store behavior for latest manifest per host, host deletion, host freshness timestamps, grouped state responses, ETag changes, and host ID collision warnings.
- Local reconciliation marks rows as materialized, missing on this host, and differing without claiming unproven cross-host ahead/behind.
- Fleet read commands publish before reading hub state, but still render reachable hub state if that best-effort publish fails.
- Publish-on-mutation hooks call the manifest publisher after successful add/remove/prune but do not fail or noticeably delay the mutation when publish fails or times out.

Integration tests should use local HTTP handlers and temporary stores. They should not require Tailscale, public network access, elevated privileges, launchd, systemd, or an external daemon.
