# kwt TUI Project Perspective Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add adaptive footer hints and an uppercase `P` project perspective switcher to the kwt TUI.

**Architecture:** Keep backend discovery unchanged. Add pure TUI helpers for project identities and footer hint rendering, then update the model to combine active perspective, lowercase project filter, and text search. Worktree creation continues to use the selected row, but selection is constrained by the active project perspective.

**Tech Stack:** Go, Bubble Tea v2, Bubbles textinput, Lip Gloss v2 table rendering, existing TUI model tests.

---

### Task 1: Adaptive Footer Help Table

**Files:**

- Create: `internal/tui/footer_hints.go`
- Modify: `internal/tui/model.go`
- Test: `internal/tui/model_test.go`

- [ ] Write failing tests that footer hints render with `▕`, fit terminal width, and reserve wrapped footer height.
- [ ] Implement `helpItem`, `renderHelpTable`, reflowing, and model footer rendering.
- [ ] Run focused TUI tests.

### Task 2: Project Perspective Switcher

**Files:**

- Modify: `internal/tui/keymap.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/model.go`
- Test: `internal/tui/list_test.go`
- Test: `internal/tui/model_test.go`

- [ ] Write failing tests for uppercase `P` switcher selection, cancel, header state, and project-scoped create.
- [ ] Implement active project perspective state and project switcher input/navigation.
- [ ] Make filtered rows apply perspective first, lowercase project filter second, text filter third.
- [ ] Run focused TUI tests.

### Task 3: Verification and PR Update

**Files:**

- All modified files above.

- [ ] Run `go test ./internal/tui`.
- [ ] Run `go test ./internal/config ./internal/cmd ./internal/tui`.
- [ ] Run `make test`.
- [ ] Run `make build`.
- [ ] Commit and push `fix/tui-launch-repo`.
