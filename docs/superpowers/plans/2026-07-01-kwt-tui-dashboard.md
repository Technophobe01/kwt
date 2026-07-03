# kwt TUI Dashboard Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the initial `kwt tui` full-screen dashboard and make bare `kwt` launch it when stdin and stdout are interactive.

**Architecture:** Put testable dashboard state and rendering in `internal/tui`, with a backend interface owned by that package. Keep real filesystem/git/tmux/shell wiring in `internal/cmd`, move status collection to `internal/status`, and hand off terminal-seizing actions only after Bubble Tea exits.

**Tech Stack:** Go, Cobra, Bubble Tea v2 (`charm.land/bubbletea/v2`), Bubbles v2 (`charm.land/bubbles/v2`), Lip Gloss v2, existing kwt discovery/status/worktree/tmux/config packages.

---

### Task 1: Dependencies and Status Collector Move

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/status/status_collector.go`
- Modify: `internal/cmd/status.go`

- [ ] **Step 1: Add Bubble Tea v2 and Bubbles v2 dependencies**

Run: `go get charm.land/bubbletea/v2@v2.0.7 charm.land/bubbles/v2@v2.1.0`

Expected: `go.mod` requires those modules and keeps `charm.land/lipgloss/v2 v2.0.2`.

- [ ] **Step 2: Move status collector**

Move `internal/cmd/status_collector.go` to `internal/status/status_collector.go`, change package name to `status`, and update `internal/cmd/status.go` imports/calls to `status.NewStatusCollectorWithOptions`.

- [ ] **Step 3: Verify move**

Run: `go test ./internal/cmd ./internal/status`

Expected: PASS.

### Task 2: Pure TUI Helpers

**Files:**

- Create: `internal/tui/backend.go`
- Create: `internal/tui/list.go`
- Create: `internal/tui/list_test.go`

- [ ] **Step 1: Write failing tests**

Cover sorting by repo then branch, filtering over repo/branch/path, compact change formatting (`+a ~m -d ?u`, `clean`, unknown `?`), compact activity formatting (`now`, `3m`, `2h`, `5d`, `3w`, `-`), path anchoring, repo fallback, and truncation.

Run: `go test ./internal/tui -run 'Test(Sort|Filter|Format|Clamp|Truncate|RowName)'`

Expected: FAIL because `internal/tui` does not exist.

- [ ] **Step 2: Implement helpers**

Define `Row`, `Backend`, `Handoff`, row identity helpers, formatter helpers, filtering, sorting, and path-anchor selection helpers. `Row.Status` must always be treated as nullable by helpers.

- [ ] **Step 3: Verify helpers**

Run: `go test ./internal/tui -run 'Test(Sort|Filter|Format|Clamp|Truncate|RowName)'`

Expected: PASS.

### Task 3: Model State Machine

**Files:**

- Create: `internal/tui/model.go`
- Create: `internal/tui/keymap.go`
- Create: `internal/tui/theme.go`
- Create: `internal/tui/model_test.go`

- [ ] **Step 1: Write failing tests**

Use `tea.KeyPressMsg` and `key.Matches`/`msg.String()`; Bubble Tea v2 `View()` returns `tea.View`. Cover initial fetch, cursor movement, filtering, escape behavior, delete/main refusal, live-session delete confirm prompt, kill-session confirm, shell handoff, outside-tmux attach handoff, inside-tmux resident attach, action errors, and path-anchored selection after refresh.

Run: `go test ./internal/tui -run TestModel`

Expected: FAIL because model code is missing.

- [ ] **Step 2: Implement model**

Implement async backend commands, single-flight refresh, text input state, confirmation state, handoff recording, status/error messages, help overlay, footer hints, and dense list rendering. Do not call `tea.WithAltScreen()`; return a `tea.View` and set `AltScreen = true` per frame.

- [ ] **Step 3: Verify model**

Run: `go test ./internal/tui -run TestModel`

Expected: PASS.

### Task 4: Command Wiring and Real Backend

**Files:**

- Create: `internal/cmd/tui.go`
- Create: `internal/cmd/tui_backend.go`
- Modify: `internal/cmd/root.go`
- Modify: `internal/cmd/open.go`
- Create or modify: `internal/cmd/tui_test.go`
- Modify: `internal/cmd/open_test.go`

- [ ] **Step 1: Write failing command wiring tests**

Cover `tuiCmd` has no-op `PersistentPreRunE`, root command skips cwd local config for bare root execution, ordinary subcommands still inherit root merge behavior, and bare root behavior preserves unknown-command errors.

Run: `go test ./internal/cmd -run 'Test(TUICmd|Root)'`

Expected: FAIL because command wiring is missing.

- [ ] **Step 2: Implement `kwt tui` command**

Add TTY guard (`kwt tui requires an interactive terminal`), load global-only config, require `worktree.basedir`, `exec.LookPath("tmux")`, validate layouts, construct backend/model, run `tea.NewProgram`, and execute final handoff after `Program.Run()` returns.

- [ ] **Step 3: Implement real backend**

List via `discovery.DiscoverGlobalWorktrees`, status via `status.CollectAll` with `FetchRemote: false`, join statuses by path, call `tmux.ListSessions()` once, guard `entry.RepositoryInfo != nil` before `tmux.WorkspaceSessionName`, create via `worktree.New(git.New(row.Entry.Path), cfg).Add(branch, "", true)`, remove first then kill live session, kill workspace, and resident attach with `WorkspaceRunner.EnsureAndAttach`.

- [ ] **Step 4: Implement root behavior**

Add `Args: cobra.NoArgs` and `RunE` to root. Bare `kwt` launches dashboard only when stdin and stdout are TTYs; otherwise it prints help. Root `PersistentPreRunE` skips `config.MergeCwdLocal()` when `cmd == rootCmd`.

- [ ] **Step 5: Verify command wiring**

Run: `go test ./internal/cmd -run 'Test(TUICmd|Root|OpenCmd)'`

Expected: PASS.

### Task 5: Full Verification

**Files:**

- All touched files

- [ ] **Step 1: Format**

Run: `gofmt -w internal/status internal/tui internal/cmd`

Expected: no output.

- [ ] **Step 2: Full test suite**

Run: `make test`

Expected: PASS.

- [ ] **Step 3: Build**

Run: `make build`

Expected: `./kwt` builds successfully.

- [ ] **Step 4: Non-TTY smoke checks**

Run: `./kwt tui </dev/null`

Expected: exits non-zero with `kwt tui requires an interactive terminal`.

Run: `./kwt </dev/null >/tmp/kwt-help.txt`

Expected: exits zero and writes help text.
