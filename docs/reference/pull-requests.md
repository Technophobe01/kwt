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
registered project name, or its main-repository path. It may be omitted when
the command runs inside the desired repository. `--state` accepts `open`,
`closed`, or `all` and defaults to `open`.

## Authentication

GitHub API authentication is resolved without prompting:

1. `KWT_GITHUB_TOKEN`, when nonempty.
2. The output of `gh auth token`.

kwt uses that token only through `go-github`; it never writes or prints the
token. Git fetch and push use normal Git remote authentication. Import fetches
with `GIT_TERMINAL_PROMPT=0`, so missing Git credentials fail instead of
blocking an embedded or SSH client. Configure a Git credential helper or SSH
authentication for subsequent pushes.

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
    "path": "/home/alice/worktrees/github.com/acme/widget/pr-17-feature-rendering",
    "state": "ready",
    "session_name": "kwt-workspace-github-com-acme-widget-pr-17-feature-rendering-a1b2c3d4"
  }
}
```

## Import contract

kwt chooses the deterministic local branch name, selects or creates the
correct source remote, fetches the head, creates the worktree through the
normal workspace manager (including repository setup hooks), and configures
plain `git push` to update the PR's original head branch. It reports the exact
tmux session name a client can attach to; import does not launch or manipulate
tmux panes.

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
      "path": "/home/alice/worktrees/github.com/acme/widget/pr-17-feature-rendering",
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
    "path": "/home/alice/worktrees/github.com/acme/widget/pr-17-feature-rendering",
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
worktree no longer exists.

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

All diagnostics go to stderr. Consumers should parse stdout and branch on
`error.code`; they never need to scrape CLI prose.
