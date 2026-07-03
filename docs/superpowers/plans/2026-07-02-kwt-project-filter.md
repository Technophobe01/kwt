# kwt Project Filter Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a durable kwt project registry and a TUI project filter so the dashboard can show worktrees from every known project.

**Architecture:** Config owns persistence and `$KWT_HOME` path resolution. The command TUI backend registers and discovers known projects. The pure TUI model only filters already supplied rows by project and text filters.

**Tech Stack:** Go, Viper TOML config, existing git/discovery/status/tmux packages, Bubble Tea v2 model tests.

---

### Task 1: Config Home and Project Registry

**Files:**

- Modify: `pkg/models/models.go`
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] Write failing tests for `KWT_HOME` taking precedence over `~/.config/kwt`.
- [ ] Write failing tests for registering a project into global `projects`.
- [ ] Implement `Project` config model and config helpers.
- [ ] Run focused config tests.

### Task 2: TUI Backend Known Project Discovery

**Files:**

- Modify: `internal/cmd/tui_backend.go`
- Test: `internal/cmd/tui_test.go`

- [ ] Write failing tests showing registered projects are included by the backend.
- [ ] Write failing tests showing launch repo registration is attempted.
- [ ] Implement registered project discovery and best-effort launch registration.
- [ ] Run focused command tests.

### Task 3: TUI Project Filter

**Files:**

- Modify: `internal/tui/keymap.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/model.go`
- Test: `internal/tui/list_test.go`
- Test: `internal/tui/model_test.go`

- [ ] Write failing tests for `p` project filter behavior.
- [ ] Implement project filter state, keymap, rendering, and helper filtering.
- [ ] Run focused TUI tests.

### Task 4: Verification and Commit

**Files:**

- All modified files above.

- [ ] Run `go test ./internal/config ./internal/cmd ./internal/tui`.
- [ ] Run `make test`.
- [ ] Run `make build`.
- [ ] Commit with a rationale-focused message.
