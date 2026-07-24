# Pull-request automation

kwt owns GitHub pull-request discovery and workspace import. A client such as a
desktop application should render the records returned by kwt and pass the
selected pull-request identifier back to kwt. It should not call GitHub,
construct Git refs or worktree names, or reproduce workspace setup.

The commands are noninteractive and always emit JSON. Pass `--json` explicitly
to document that a caller depends on the automation contract:

```sh
kwt pr list --project github.com/acme/widget --state open --json
kwt pr import github:github.com/acme/widget#17 \
  --project github.com/acme/widget --json
```

`--project` accepts a repository identity from `kwt projects --json`, a
registered project name, or its absolute canonical main-repository path.
Identity and unique-name matching take precedence over path matching; relative
and symlinked path selectors are rejected. `--project` may be omitted when the
command runs inside the desired repository. If a display name identifies
multiple projects, kwt returns `repository_mismatch`; callers must use the
repository identity or path. `--state` accepts `open`, `closed`, or `all` and
defaults to `open`.

## Authentication

GitHub API authentication is resolved without prompting:

1. `KWT_GITHUB_TOKEN`, when nonempty.
2. The output of `gh auth token`.

kwt uses that token only through `go-github`; it never writes or prints the
token. Git fetch and push use normal Git remote authentication. Import fetches
with `GIT_TERMINAL_PROMPT=0`, so missing Git credentials fail instead of
blocking an embedded or SSH client. Configure a Git credential helper or SSH
authentication for subsequent pushes.

Import stops before mutation when a repository stores credentials directly in
a remote fetch or push URL. Use a Git credential helper or SSH agent instead;
this keeps contributor-triggered Git operations from reading reusable
credentials out of the linked worktree's shared config.
Validation includes configured include files and checks Git's effective fetch
and push URLs after `insteadOf` and `pushInsteadOf` rewriting. Remote URLs with
query strings or fragments are rejected, as are invalid scheme-based URLs and
opaque remote-helper (`transport::address`) URLs.

Import fetches also force the SSH implementation's noninteractive mode
(OpenSSH batch mode or PuTTY/plink's equivalent) and disable askpass-style
credential prompts. Every ref-mutating import operation—including fetch,
checkout, and rollback—runs with the same sanitized environment and an empty
trusted hooks directory. Checkout additionally disables every configured
smudge/process filter. Repository hooks, filters, copied files, and setup
commands therefore do not run during PR import: environment scrubbing alone
cannot prevent same-user processes from reading kwt configuration or token
files from disk.

Import requires Git 2.20 or newer because kwt uses per-worktree Git
configuration to make plain `git push` target the PR head without changing
push behavior in the main checkout. kwt checks this requirement before it
fetches refs, adds remotes, or creates a worktree.

## Listing contract

```json
{
  "pull_requests": [
    {
      "id": "github:github.com/acme/widget#17",
      "provider": "github",
      "repository": {
        "provider": "github",
        "identity": "github.com/acme/widget",
        "host": "github.com",
        "owner": "acme",
        "name": "widget"
      },
      "number": 17,
      "url": "https://github.com/acme/widget/pull/17",
      "title": "Improve widget rendering",
      "author": "octocat",
      "source": {
        "branch": "feature/rendering",
        "repository": {
          "provider": "github",
          "identity": "github.com/octocat/widget",
          "host": "github.com",
          "owner": "octocat",
          "name": "widget"
        },
        "is_fork": true
      },
      "target": {
        "branch": "main",
        "repository": {
          "provider": "github",
          "identity": "github.com/acme/widget",
          "host": "github.com",
          "owner": "acme",
          "name": "widget"
        },
        "is_fork": false
      },
      "draft": true,
      "state": "open",
      "head_sha": "0123456789abcdef0123456789abcdef01234567",
      "imported": false
    }
  ]
}
```

The opaque `id` is stable for the provider, base repository, and PR number.
Import also accepts a PR URL or a number scoped by `--project`.

An imported list result adds the canonical workspace record:

```json
{
  "id": "github:github.com/acme/widget#17",
  "provider": "github",
  "repository": {
    "provider": "github",
    "identity": "github.com/acme/widget",
    "host": "github.com",
    "owner": "acme",
    "name": "widget"
  },
  "number": 17,
  "url": "https://github.com/acme/widget/pull/17",
  "title": "Improve widget rendering",
  "author": "octocat",
  "source": {
    "branch": "feature/rendering",
    "repository": {
      "provider": "github",
      "identity": "github.com/octocat/widget",
      "host": "github.com",
      "owner": "octocat",
      "name": "widget"
    },
    "is_fork": true
  },
  "target": {
    "branch": "main",
    "repository": {
      "provider": "github",
      "identity": "github.com/acme/widget",
      "host": "github.com",
      "owner": "acme",
      "name": "widget"
    },
    "is_fork": false
  },
  "draft": false,
  "state": "open",
  "head_sha": "0123456789abcdef0123456789abcdef01234567",
  "imported": true,
  "workspace": {
    "id": "github.com/acme/widget:pr-17-feature-rendering:a1b2c3d4",
    "repository": "github.com/acme/widget",
    "branch": "pr-17-feature-rendering",
    "path": "/home/alice/.kwt/worktrees/github.com/acme/widget/pr-17-feature-rendering",
    "state": "ready",
    "session_name": "kwt-workspace-github-com-acme-widget-pr-17-feature-rendering-a1b2c3d4"
  }
}
```

## Import contract

kwt chooses the deterministic local branch name, selects or creates a clean
source remote, fetches the head, creates a no-checkout worktree, materializes
it with external filters disabled, and configures plain `git push` to update
exactly the PR's original head branch. Unlike an ordinary `kwt add`, PR import
does not apply `copy_files` or `setup_commands`; run any desired project setup
explicitly after reviewing the imported files.
When it creates a fork remote, kwt uses SSH when the project's effective push
URL uses SSH and HTTPS otherwise, preserving the project's working push
authentication transport, including SSH host aliases and explicit ports. It
reuses only remotes with exactly one effective push URL and no custom push
refspec. Worktree-local `pushRemote` configuration takes precedence over global
push defaults. Import reports the exact tmux session name a client can attach
to; it does not launch or manipulate tmux panes.

Cross-project imports load the selected project's already trusted `.kwt.toml`
in isolation. They never load configuration from the caller's working
directory and never prompt or auto-trust in this automation path. Repository
`copy_files` and `setup_commands` are deliberately ignored for PR imports. A
target repository's `.kwt.toml` must be a
regular file, not a symlink, so trust granted to another path cannot be reused.
The registered project path must resolve to that repository's main Git root;
empty, relative, missing, subdirectory, and linked-worktree paths are rejected
before target configuration is loaded.

Configured file copies use rooted destination operations and reject symlinks
in the destination path, preventing contributor-controlled checkout entries
from redirecting writes outside the new worktree. Relative paths in a trusted
target `.kwt.toml` are resolved against that target repository, never the
caller's working directory, and target-local path fields cannot expand
environment variables into workspace paths.

Before materializing pull-request files, kwt creates a no-checkout worktree and
verifies that branch- and worktree-conditional Git includes do not change its
effective configuration, including record order and precedence. Push URLs and
refspecs are validated against the PR source repository again after push
configuration, and import fails if `HEAD` no longer names the generated
workspace branch. Worktree paths are stored and matched in canonical form so
symlinked base directories do not create duplicate imports.

A new import returns:

```json
{
  "status": "created",
  "pull_request": {
    "id": "github:github.com/acme/widget#17",
    "provider": "github",
    "repository": {
      "provider": "github",
      "identity": "github.com/acme/widget",
      "host": "github.com",
      "owner": "acme",
      "name": "widget"
    },
    "number": 17,
    "url": "https://github.com/acme/widget/pull/17",
    "title": "Improve widget rendering",
    "author": "octocat",
    "source": {
      "branch": "feature/rendering",
      "repository": {
        "provider": "github",
        "identity": "github.com/octocat/widget",
        "host": "github.com",
        "owner": "octocat",
        "name": "widget"
      },
      "is_fork": true
    },
    "target": {
      "branch": "main",
      "repository": {
        "provider": "github",
        "identity": "github.com/acme/widget",
        "host": "github.com",
        "owner": "acme",
        "name": "widget"
      },
      "is_fork": false
    },
    "draft": false,
    "state": "open",
    "head_sha": "0123456789abcdef0123456789abcdef01234567",
    "imported": true,
    "workspace": {
      "id": "github.com/acme/widget:pr-17-feature-rendering:a1b2c3d4",
      "repository": "github.com/acme/widget",
      "branch": "pr-17-feature-rendering",
      "path": "/home/alice/.kwt/worktrees/github.com/acme/widget/pr-17-feature-rendering",
      "state": "ready",
      "session_name": "kwt-workspace-github-com-acme-widget-pr-17-feature-rendering-a1b2c3d4"
    }
  },
  "project": {
    "identity": "github.com/acme/widget",
    "name": "widget",
    "path": "/home/alice/src/widget"
  },
  "workspace": {
    "id": "github.com/acme/widget:pr-17-feature-rendering:a1b2c3d4",
    "repository": "github.com/acme/widget",
    "branch": "pr-17-feature-rendering",
    "path": "/home/alice/.kwt/worktrees/github.com/acme/widget/pr-17-feature-rendering",
    "state": "ready",
    "session_name": "kwt-workspace-github-com-acme-widget-pr-17-feature-rendering-a1b2c3d4"
  }
}
```

Repeating the same import returns the same shape and workspace with
`"status": "already_imported"`. Provenance is stored in
`$KWT_HOME/pull-requests.json` (or `~/.config/kwt/pull-requests.json`) and is
updated under a cross-process file lock. The lock covers checking, fetching,
creating, configuring, and recording, so concurrent imports converge on one
workspace. A stale provenance record is not reported as imported when its Git
worktree no longer exists. Existing imports require complete source provenance
and a matching live worktree path and branch before `already_imported` is
returned. KWT does not push during this check or rewrite Git remotes and routing
that the local user changed after import.

If an import fails after creating a workspace, kwt rolls it back even when the
request context was canceled. Request cancellation, including `SIGINT` and
`SIGTERM`, terminates checkout; cleanup then runs without the canceled context.
Worktree creation retains an ownership reservation through late rollback and
removes only the original directory identity, a clean worktree, and the
unchanged reserved ref. A replaced path, dirty worktree, or advanced branch is
preserved and reported for manual cleanup. If the PR's recorded source
repository or branch changed while its imported workspace is still present,
another import returns `import_conflict`.

## Failure contract

Failures write a JSON error to stdout, a credential-free diagnostic to stderr,
and return a stable nonzero status. For example:

```json
{
  "error": {
    "code": "authentication_failed",
    "message": "GitHub authentication failed",
    "retryable": false
  }
}
```

| Exit | Error code                     | Meaning |
| ---: | ------------------------------ | ------- |
| 2    | `invalid_pull_request_selector` | Invalid state, URL, opaque ID, or number. |
| 3    | `authentication_failed`         | GitHub API or Git authentication failed. |
| 4    | `repository_mismatch` / `unsupported_provider` | Project selection or provider mismatch. |
| 5    | `pull_request_not_found`        | The selected PR or repository is missing. |
| 6    | `inaccessible_head`             | The fork or source branch is unavailable. |
| 7    | `naming_conflict`               | The generated branch or workspace is occupied. |
| 8    | `network_failure`               | A retryable provider or Git network failure. |
| 9    | `workspace_creation_failed`     | Worktree creation, setup, push config, or persistence failed. |
| 10   | `malformed_provider_response`   | GitHub returned an invalid success response. |
| 11   | `import_conflict`               | Concurrent state or the selected head SHA changed. |
| 12   | `unsupported_git_version`        | Git is too old for isolated per-worktree push configuration. |

GitHub primary and secondary rate limits, including HTTP 429 responses, use
`network_failure` with `retryable: true`.

All diagnostics go to stderr. Consumers should parse stdout and branch on
`error.code`; they never need to scrape CLI prose.
