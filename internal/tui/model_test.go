package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/pkg/models"
)

type fakeBackend struct {
	rows            []Row
	layoutNames     []string
	insideTmux      bool
	createPath      string
	createErr       error
	materializePath string
	materializeErr  error
	removeErr       error
	killErr         error
	openErr         error
	listCalls       int
	createCalls     []string
	materializeRows []string
	removeCalls     []string
	removeForces    []bool
	killCalls       []string
	openCalls       []string
	unregistered    []Row
}

func (b *fakeBackend) List(ctx context.Context) ([]Row, []string, error) {
	b.listCalls++
	return append([]Row(nil), b.rows...), nil, nil
}

func (b *fakeBackend) CreateWorktree(ctx context.Context, row Row, branch string) (string, error) {
	b.createCalls = append(b.createCalls, rowPath(row)+":"+branch)
	return b.createPath, b.createErr
}

func (b *fakeBackend) RemoveWorktree(ctx context.Context, row Row, force bool) error {
	b.removeCalls = append(b.removeCalls, rowPath(row))
	b.removeForces = append(b.removeForces, force)
	return b.removeErr
}

func (b *fakeBackend) MaterializeWorktree(ctx context.Context, row Row) (string, error) {
	if row.Fleet != nil {
		b.materializeRows = append(b.materializeRows, row.Fleet.ProjectIdentity+":"+row.Fleet.Ref)
	}
	return b.materializePath, b.materializeErr
}

func (b *fakeBackend) KillSession(row Row) error {
	b.killCalls = append(b.killCalls, row.SessionName)
	return b.killErr
}

func (b *fakeBackend) OpenInTmux(ctx context.Context, row Row, layoutName string) error {
	b.openCalls = append(b.openCalls, rowPath(row)+":"+layoutName)
	return b.openErr
}

func (b *fakeBackend) UnregisterWorkspace(row Row) error {
	b.unregistered = append(b.unregistered, row)
	return nil
}

func (b *fakeBackend) LayoutNames() []string { return append([]string(nil), b.layoutNames...) }

func (b *fakeBackend) InsideTmux() bool { return b.insideTmux }

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	default:
		runes := []rune(s)
		return tea.KeyPressMsg(tea.Key{Code: runes[0], Text: s})
	}
}

func paste(s string) tea.PasteMsg {
	return tea.PasteMsg{Content: s}
}

func updateModel(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	m, ok := next.(Model)
	require.True(t, ok)
	return m, cmd
}

func viewContent(model Model) string {
	return model.View().Content
}

func TestWorkspaceRowActions(t *testing.T) {
	row := Row{
		Workspace:   &WorkspaceInfo{Name: "notes", Path: "/Users/me/notes"},
		SessionName: "kwt-workspace-dir-notes-12345678",
		SessionLive: true,
	}
	backend := &fakeBackend{}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	// Open: workspace rows hand off to attach outside tmux.
	next, _ := model.openSelected()
	assert.Equal(t, HandoffAttach, next.Handoff().Kind)

	// New branch: gated with a message.
	next, _ = model.startNewBranch()
	assert.Contains(t, next.message, "not a git worktree")

	// Sync: already gated by the row.Fleet == nil branch.
	next, _ = model.syncSelected(row)
	assert.Contains(t, next.message, "nothing to sync")

	// Kill: allowed for live sessions.
	next, _ = model.startKill()
	assert.Equal(t, confirmKill, next.confirm.kind)

	// Delete key: offers unregister, never worktree removal.
	next, _ = model.startDelete()
	require.Equal(t, confirmUnregister, next.confirm.kind)
	assert.Contains(t, next.confirm.text, "unregister")

	// Confirming with y calls Backend.UnregisterWorkspace and refreshes.
	_, cmd := updateModel(t, next, press("y"))
	require.NotNil(t, cmd)
	msg := cmd()
	done, ok := msg.(actionDoneMsg)
	require.True(t, ok)
	assert.True(t, done.refresh)
	require.Len(t, backend.unregistered, 1)
	assert.Equal(t, "notes", backend.unregistered[0].Workspace.Name)
}

func TestModelRowsMessageSortsRendersAndUsesAltScreen(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "zeta", "/w/kwt/zeta"),
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "alpha", "/w/kwt/alpha"),
	}})

	content := viewContent(model)
	assert.Contains(t, content, "kwt · 3 worktrees · 2 repos")
	assert.Contains(t, content, "/ search")
	assert.Contains(t, content, "L layout:default")
	assert.Contains(t, content, "? help")
	assert.Less(t, strings.Index(content, "kata"), strings.Index(content, "alpha"))
	assert.True(t, model.View().AltScreen)
}

func TestModelRendersBackendWarnings(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{
		rows:     []Row{testRow("kwt", "main", "/w/kwt/main")},
		warnings: []string{`multiple machines are publishing as host ID "same" (host same)`},
	})

	content := viewContent(model)
	assert.Contains(t, content, `warning: multiple machines are publishing as host ID "same" (host same)`)

	model, _ = updateModel(t, model, rowsMsg{
		rows: []Row{testRow("kwt", "main", "/w/kwt/main")},
	})
	assert.NotContains(t, viewContent(model), "warning:",
		"warnings must clear once the hub state is healthy again")
}

func TestRenderHelpTableReflowsToFitWidth(t *testing.T) {
	got := stripANSI(renderHelpTable(defaultHelpRows(Row{}), 34))
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	require.Greater(t, len(lines), 1)
	assert.Contains(t, got, "▕")
	assert.Contains(t, got, "P project")
	for _, line := range lines {
		assert.LessOrEqual(t, len([]rune(line)), 34, line)
	}
}

func TestModelFooterUsesAdaptiveHelpTable(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 70, Height: 12})
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	content := stripANSI(viewContent(model))

	assert.Contains(t, content, "▕")
	assert.Contains(t, content, "P project")
	assert.NotContains(t, content, "K kill-ws  p project")
}

func TestModelSelectsCurrentWorktreeOnInitialRows(t *testing.T) {
	current := testRow("zzz-other", "main", "/repos/other")
	current.Status.IsCurrent = true
	model := NewModel(&fakeBackend{}, "/worktrees")

	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		current,
	}})

	assert.Equal(t, "/repos/other", rowPath(model.selectedRow()))
}

func TestModelPolishesSingleRowDashboard(t *testing.T) {
	row := testRow("kwt", "test/layouts", "/worktrees/github.com/example/kwt/test-layouts")
	row.SessionLive = true
	model := NewModel(&fakeBackend{layoutNames: []string{"quad", "focus"}}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	content := viewContent(model)

	assert.Contains(t, content, "kwt · 1 worktree · 1 repo")
	assert.Contains(t, content, "WORKSPACE")
	assert.Contains(t, content, "live")
	assert.Contains(t, content, "\x1b[92mlive")
	assert.Contains(t, stripANSI(content), "layout default")
	assert.Contains(t, content, "selected kwt:test/layouts")
	assert.Contains(t, content, "/worktrees/github.com/example/kwt/test-layouts")
	assert.Contains(t, stripANSI(content), "c shell")
	assert.NotContains(t, stripANSI(content), "s shell")
	assert.NotContains(t, stripANSI(content), "s sync")
}

func TestModelRendersRemoteOnlyFleetRows(t *testing.T) {
	row := Row{Fleet: &FleetInfo{
		ProjectIdentity:  "github.com/example/kwt",
		ProjectName:      "kwt",
		Kind:             "branch",
		Ref:              "feature/studio-only",
		Branch:           "feature/studio-only",
		Local:            false,
		Hosts:            []string{"host-b"},
		Sync:             "same",
		Dirty:            "clean",
		Freshness:        "just now",
		MaterializeHost:  "host-b",
		RemotePath:       "/work/host-b/kwt/feature-studio-only",
		RemoteHead:       "bbb",
		RemoteUpstream:   "origin/feature/studio-only",
		RemoteAhead:      2,
		CanMaterialize:   true,
		MaterializeLabel: "feature/studio-only",
	}}
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	content := stripANSI(viewContent(model))

	assert.NotContains(t, content, "MACHINES")
	assert.Contains(t, content, "kwt")
	assert.Contains(t, content, "feature/studio-only")
	assert.Contains(t, content, "remote only")
	assert.NotContains(t, content, "hosts same")
	assert.Contains(t, content, "remote")
	assert.Contains(t, content, "s sync")
	assert.NotContains(t, content, "c shell")
	assert.NotContains(t, content, "m materialize")
	assert.Contains(t, content, "selected kwt:feature/studio-only")
	assert.Contains(t, content, "remote on host-b")
	assert.Contains(t, content, "press s to sync (branch must be pushed/fetched here)")
	assert.Contains(t, content, "source is 2 commits ahead of origin/feature/studio-only")
	assert.Contains(t, content, "/work/host-b/kwt/feature-studio-only")
}

func TestModelDashboardFitsHundredColumnTerminal(t *testing.T) {
	row := testRow("example-service", "very-long-feature-branch-that-needs-truncation", "/w/example-service/feature")
	row.SessionLive = true
	row.Status.LastActivity = timeNow().Add(-8 * time.Hour)
	row.Status.GitStatus.Modified = 3
	row.Fleet = &FleetInfo{
		ProjectName: "example-service",
		Local:       true,
		Hosts:       []string{"local", "host-b"},
		Sync:        "different: host-b 18h",
		Dirty:       "host-b (~3 ?3)",
		Freshness:   "18h",
	}
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 12})
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	lines := strings.Split(stripANSI(viewContent(model)), "\n")
	header := findLineContaining(lines, "REPO")
	body := findLineContaining(lines, "diff host-b")

	require.NotEmpty(t, header)
	require.NotEmpty(t, body)
	assert.Contains(t, header, "WORKSPACE")
	assert.NotContains(t, header, "MACHINES")
	assert.Contains(t, body, "live")
	content := stripANSI(viewContent(model))
	assert.Contains(t, content, "also on host-b")
	assert.Contains(t, content, "head differs 18h")
	assert.Contains(t, content, "remote changes ~3 ?3")
	assert.NotContains(t, content, "machines local")
	assert.LessOrEqual(t, visibleWidth(header), 100, header)
	assert.LessOrEqual(t, visibleWidth(body), 100, body)
}

func TestModelSummarizesRemoteChangesInTableAndDetailsNameHost(t *testing.T) {
	row := testRow("kwt", "feature/remote-dirty", "/w/kwt/feature")
	row.Fleet = &FleetInfo{
		ProjectName: "kwt",
		Local:       true,
		Hosts:       []string{"local", "host-b"},
		Sync:        "same",
		Dirty:       "host-b (~3 ?3)",
		Freshness:   "5m",
	}
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	lines := strings.Split(stripANSI(viewContent(model)), "\n")
	body := findLineContaining(lines, "feature/remote-dirty")

	require.NotEmpty(t, body)
	assert.Contains(t, body, "remote ~3 ?3")
	assert.NotContains(t, body, "host-b")
	content := stripANSI(viewContent(model))
	assert.Contains(t, content, "also on host-b")
	assert.Contains(t, content, "remote changes ~3 ?3")
	assert.NotContains(t, content, "changes host-b")
}

func TestModelCyclesLayoutSelection(t *testing.T) {
	model := NewModel(&fakeBackend{layoutNames: []string{"quad", "focus"}}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{testRow("kwt", "main", "/w/kwt/main")}})

	assert.Contains(t, stripANSI(viewContent(model)), "layout default")
	footerLine := lineIndexContaining(strings.Split(stripANSI(viewContent(model)), "\n"), "q quit")

	model, _ = updateModel(t, model, press("L"))
	assert.Equal(t, "quad", model.selectedLayout)
	assert.Contains(t, stripANSI(viewContent(model)), "selected kwt:main · layout quad · workspace offline")
	assert.Contains(t, viewContent(model), "layout \x1b")
	assert.Contains(t, viewContent(model), "/w/kwt/main")
	assert.Equal(t, footerLine, lineIndexContaining(strings.Split(stripANSI(viewContent(model)), "\n"), "q quit"))

	model, _ = updateModel(t, model, press("L"))
	assert.Equal(t, "focus", model.selectedLayout)
	assert.Contains(t, stripANSI(viewContent(model)), "selected kwt:main · layout focus · workspace offline")
	assert.Equal(t, footerLine, lineIndexContaining(strings.Split(stripANSI(viewContent(model)), "\n"), "q quit"))

	model, _ = updateModel(t, model, press("L"))
	assert.Empty(t, model.selectedLayout)
	assert.Contains(t, stripANSI(viewContent(model)), "selected kwt:main · layout default · workspace offline")
	assert.Equal(t, footerLine, lineIndexContaining(strings.Split(stripANSI(viewContent(model)), "\n"), "q quit"))
}

func TestModelCursorFilterAndEscape(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		testRow("kata", "feature", "/w/kata/feature"),
	}})

	model, _ = updateModel(t, model, press("j"))
	assert.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))

	model, _ = updateModel(t, model, press("/"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("w"))
	assert.Equal(t, "kw", model.filter)
	assert.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))
	assert.Contains(t, viewContent(model), "/ filter:")
	assert.Contains(t, viewContent(model), "kw")

	model, _ = updateModel(t, model, press("esc"))
	assert.Empty(t, model.filter)
	assert.Contains(t, viewContent(model), "/w/kwt/main")
}

func TestModelTextFilterAcceptsPaste(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		testRow("kata", "feature", "/w/kata/feature"),
	}})

	model, _ = updateModel(t, model, press("/"))
	model, _ = updateModel(t, model, paste("kata"))

	assert.Equal(t, "kata", model.filter)
	assert.Equal(t, "/w/kata/feature", rowPath(model.selectedRow()))
	assert.Contains(t, stripANSI(viewContent(model)), "/ filter: kata")
}

func TestModelProjectFilterNarrowsRows(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kata", "main", "/w/kata/main"),
	}})

	model, _ = updateModel(t, model, press("p"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("a"))
	model, _ = updateModel(t, model, press("t"))

	assert.Equal(t, "kat", model.projectFilter)
	assert.Equal(t, "/w/kata/main", rowPath(model.selectedRow()))
	content := viewContent(model)
	assert.Contains(t, content, "p filter:")
	assert.Contains(t, content, "p filter:kat")
	assert.Contains(t, content, "kata")
	assert.NotContains(t, content, "kwt            feature")
}

func TestModelProjectFilterCombinesWithTextFilter(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kata", "feature", "/w/kata/feature"),
	}})

	model, _ = updateModel(t, model, press("p"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("w"))
	model, _ = updateModel(t, model, press("enter"))
	model, _ = updateModel(t, model, press("/"))
	model, _ = updateModel(t, model, press("f"))
	model, _ = updateModel(t, model, press("e"))
	model, _ = updateModel(t, model, press("a"))

	assert.Equal(t, "kw", model.projectFilter)
	assert.Equal(t, "fea", model.filter)
	assert.Equal(t, "/w/kwt/feature", rowPath(model.selectedRow()))
	content := viewContent(model)
	assert.Contains(t, content, "kwt            feature")
	assert.NotContains(t, content, "kwt            main")
	assert.NotContains(t, content, "kata           feature")
}

func TestModelEscapeClearsProjectFilterWhenTextFilterEmpty(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		testRow("kata", "main", "/w/kata/main"),
	}})

	model, _ = updateModel(t, model, press("p"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("a"))
	model, _ = updateModel(t, model, press("enter"))
	require.Equal(t, "ka", model.projectFilter)

	model, _ = updateModel(t, model, press("esc"))

	assert.Empty(t, model.projectFilter)
	assert.Len(t, model.filteredRows(), 2)
}

func TestModelProjectPerspectiveSwitcherNarrowsRows(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})
	model, _ = updateModel(t, model, press("j"))
	require.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))

	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("a"))
	model, _ = updateModel(t, model, press("t"))
	model, _ = updateModel(t, model, press("enter"))

	assert.Equal(t, "kata", model.projectPerspective)
	assert.Equal(t, "/w/kata/main", rowPath(model.selectedRow()))
	content := viewContent(model)
	assert.Contains(t, content, "P project:kata")
	assert.Contains(t, content, "kata")
	assert.NotContains(t, content, "kwt            main")
}

func TestModelProjectPerspectivePickerAcceptsPaste(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, paste("kat"))
	model, _ = updateModel(t, model, press("enter"))

	assert.Equal(t, "kata", model.projectPerspective)
	assert.Equal(t, "/w/kata/main", rowPath(model.selectedRow()))
}

func TestModelProjectPerspectivePickerUsesArrowKeys(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, press("down"))
	model, _ = updateModel(t, model, press("down"))
	model, _ = updateModel(t, model, press("enter"))

	assert.Equal(t, "kwt", model.projectPerspective)
	assert.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))
}

func TestModelProjectPerspectivePickerRendersModalList(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 12})
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	model, _ = updateModel(t, model, press("P"))

	content := stripANSI(viewContent(model))
	assert.Contains(t, content, "Project")
	assert.Contains(t, content, "Search: Type to search")
	assert.Contains(t, content, "All (3)")
	assert.Contains(t, content, "kata (1)")
	assert.Contains(t, content, "kwt (2)")
	assert.Contains(t, content, "↑↓ select")
	assert.NotContains(t, content, "REPO           BRANCH")
}

func TestModelProjectPerspectivePickerDisambiguatesDuplicateRepoNames(t *testing.T) {
	left := testRow("service", "main", "/w/github.com/org-one/service/main")
	left.Entry.RepositoryInfo.Host = "github.com"
	left.Entry.RepositoryInfo.Owner = "org-one"
	left.Entry.RepositoryInfo.FullPath = "github.com/org-one/service"
	left.Status.Repository = "github.com/org-one/service"
	right := testRow("service", "main", "/w/github.com/org-two/service/main")
	right.Entry.RepositoryInfo.Host = "github.com"
	right.Entry.RepositoryInfo.Owner = "org-two"
	right.Entry.RepositoryInfo.FullPath = "github.com/org-two/service"
	right.Status.Repository = "github.com/org-two/service"

	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{left, right}})
	model, _ = updateModel(t, model, press("P"))

	content := stripANSI(viewContent(model))
	assert.Contains(t, content, "github.com/org-one/service (1)")
	assert.Contains(t, content, "github.com/org-two/service (1)")

	model, _ = updateModel(t, model, paste("org-two"))
	model, _ = updateModel(t, model, press("enter"))

	assert.Equal(t, "github.com/org-two/service", model.projectPerspective)
	assert.Equal(t, "/w/github.com/org-two/service/main", rowPath(model.selectedRow()))
	assert.Len(t, model.filteredRows(), 1)
}

func TestModelProjectPerspectivePickerShowsErrors(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
	}})
	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, rowsMsg{err: errors.New("refresh exploded")})

	assert.Contains(t, stripANSI(viewContent(model)), "refresh exploded")
}

func TestModelProjectPerspectiveCancelKeepsCurrentProject(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})
	model.projectPerspective = "kwt"

	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("a"))
	model, _ = updateModel(t, model, press("esc"))

	assert.Equal(t, "kwt", model.projectPerspective)
	assert.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))
}

func TestModelNewBranchUsesProjectPerspective(t *testing.T) {
	backend := &fakeBackend{createPath: "/w/kata/new-work"}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})
	model, _ = updateModel(t, model, press("j"))
	require.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))

	model, _ = updateModel(t, model, press("P"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("a"))
	model, _ = updateModel(t, model, press("t"))
	model, _ = updateModel(t, model, press("enter"))
	model, _ = updateModel(t, model, press("n"))
	model, _ = updateModel(t, model, press("f"))
	model, _ = updateModel(t, model, press("e"))
	model, _ = updateModel(t, model, press("a"))
	_, cmd := updateModel(t, model, press("enter"))

	require.NotNil(t, cmd)
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"/w/kata/main:fea"}, backend.createCalls)
}

func TestModelNewBranchAcceptsPaste(t *testing.T) {
	backend := &fakeBackend{createPath: "/w/kwt/pasted"}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	model, _ = updateModel(t, model, press("n"))
	model, _ = updateModel(t, model, paste("feature/pasted"))
	_, cmd := updateModel(t, model, press("enter"))

	require.NotNil(t, cmd)
	_ = cmd()
	assert.Equal(t, []string{"/w/kwt/main:feature/pasted"}, backend.createCalls)
}

func TestModelMaterializeRemoteOnlyFleetRow(t *testing.T) {
	row := Row{Fleet: &FleetInfo{
		ProjectIdentity: "github.com/example/kwt",
		ProjectName:     "kwt",
		Kind:            "branch",
		Ref:             "feature/studio-only",
		Branch:          "feature/studio-only",
		Hosts:           []string{"host-b"},
		CanMaterialize:  true,
	}}
	backend := &fakeBackend{materializePath: "/worktrees/github.com/example/kwt/feature-studio-only"}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, cmd := updateModel(t, model, press("s"))

	require.NotNil(t, cmd)
	assert.Contains(t, model.message, "syncing kwt:feature/studio-only")
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"github.com/example/kwt:feature/studio-only"}, backend.materializeRows)
	done := msg.(actionDoneMsg)
	assert.True(t, done.refresh)
	assert.Equal(t, "/worktrees/github.com/example/kwt/feature-studio-only", done.anchorPath)
	assert.Contains(t, done.message, "synced kwt:feature/studio-only")
}

func TestModelCancelNewBranchInputKeepsExistingFilter(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "main", "/w/kwt/main"),
		testRow("kata", "feature", "/w/kata/feature"),
	}})
	model, _ = updateModel(t, model, press("/"))
	model, _ = updateModel(t, model, press("k"))
	model, _ = updateModel(t, model, press("w"))
	model, _ = updateModel(t, model, press("enter"))
	require.Equal(t, "kw", model.filter)

	model, _ = updateModel(t, model, press("n"))
	model, _ = updateModel(t, model, press("esc"))

	assert.Equal(t, "kw", model.filter)
	assert.Equal(t, "/w/kwt/main", rowPath(model.selectedRow()))
}

func TestModelDeleteRefusesMainWorktree(t *testing.T) {
	row := testRow("kwt", "main", "/w/kwt/main")
	row.Entry.IsMain = true
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, cmd := updateModel(t, model, press("d"))

	require.Nil(t, cmd)
	assert.Contains(t, viewContent(model), "refusing to remove a main worktree")
}

func TestModelDeleteLiveWorktreeConfirmsAndCallsRemove(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	row.SessionLive = true
	row.SessionName = "kwt-workspace-kwt-feature"
	backend := &fakeBackend{}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("d"))
	assert.Contains(t, viewContent(model), "delete kwt:feature and kill its live workspace? [y/N]")

	_, cmd := updateModel(t, model, press("y"))
	require.NotNil(t, cmd)
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"/w/kwt/feature"}, backend.removeCalls)
	assert.Equal(t, []bool{false}, backend.removeForces)
}

func TestModelDeleteDirtyWorktreeConfirmsDiscardAndForcesRemove(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	row.Status.Status = models.WorktreeStatusModified
	row.Status.GitStatus.Modified = 1
	backend := &fakeBackend{}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("d"))
	content := stripANSI(viewContent(model))
	assert.Contains(t, content, "discard changes and delete kwt:feature? [y/N]")
	assert.NotContains(t, content, "kwt remove --force")

	_, cmd := updateModel(t, model, press("y"))
	require.NotNil(t, cmd)
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"/w/kwt/feature"}, backend.removeCalls)
	assert.Equal(t, []bool{true}, backend.removeForces)
}

func TestModelDeleteDirtyLiveWorktreeConfirmsDiscardAndKill(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	row.SessionLive = true
	row.SessionName = "kwt-workspace-kwt-feature"
	row.Status.Status = models.WorktreeStatusModified
	row.Status.GitStatus.Untracked = 1
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("d"))

	assert.Contains(t, stripANSI(viewContent(model)), "discard changes, delete kwt:feature, and kill its live workspace? [y/N]")
}

func TestModelKillWorkspaceConfirm(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	row.SessionLive = true
	row.SessionName = "kwt-workspace-kwt-feature"
	backend := &fakeBackend{}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("K"))
	assert.Contains(t, viewContent(model), "kill workspace for kwt:feature? [y/N]")

	_, cmd := updateModel(t, model, press("y"))
	require.NotNil(t, cmd)
	_ = cmd()
	assert.Equal(t, []string{"kwt-workspace-kwt-feature"}, backend.killCalls)
}

func TestModelShellAndAttachHandoffsQuitFirst(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	model := NewModel(&fakeBackend{layoutNames: []string{"quad", "focus"}}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("c"))
	assert.Equal(t, HandoffShell, model.Handoff().Kind)
	assert.Equal(t, "/w/kwt/feature", rowPath(model.Handoff().Row))

	model = NewModel(&fakeBackend{layoutNames: []string{"quad", "focus"}}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})
	model, _ = updateModel(t, model, press("L"))
	model, _ = updateModel(t, model, press("L"))
	model, _ = updateModel(t, model, press("enter"))
	assert.Equal(t, HandoffAttach, model.Handoff().Kind)
	assert.Equal(t, "/w/kwt/feature", rowPath(model.Handoff().Row))
	assert.Equal(t, "focus", model.Handoff().LayoutName)
}

func TestModelSyncKeyDoesNotOpenShellForLocalRow(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, cmd := updateModel(t, model, press("s"))

	require.Nil(t, cmd)
	assert.Equal(t, HandoffNone, model.Handoff().Kind)
	assert.Contains(t, stripANSI(viewContent(model)), "nothing to sync for this row")
}

func TestModelInsideTmuxAttachRunsResidentAction(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	backend := &fakeBackend{insideTmux: true, layoutNames: []string{"quad"}}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})

	model, _ = updateModel(t, model, press("L"))
	model, cmd := updateModel(t, model, press("enter"))
	require.NotNil(t, cmd)
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"/w/kwt/feature:quad"}, backend.openCalls)
	assert.Equal(t, HandoffNone, model.Handoff().Kind)
}

func TestModelActionErrorDisplaysAndClearsOnNextKey(t *testing.T) {
	row := testRow("kwt", "feature", "/w/kwt/feature")
	row.SessionLive = true
	backend := &fakeBackend{killErr: errors.New("tmux exploded")}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{row}})
	model, _ = updateModel(t, model, press("K"))
	model, cmd := updateModel(t, model, press("y"))

	require.NotNil(t, cmd)
	model, _ = updateModel(t, model, cmd())
	assert.Contains(t, viewContent(model), "tmux exploded")

	model, _ = updateModel(t, model, press("j"))
	assert.NotContains(t, viewContent(model), "tmux exploded")
}

func TestModelQueuesActionRefreshWhileFetchInFlight(t *testing.T) {
	backend := &fakeBackend{
		rows: []Row{testRow("kwt", "feature", "/w/kwt/feature")},
	}
	model := NewModel(backend, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: backend.rows})

	model, refreshCmd := updateModel(t, model, press("r"))
	require.NotNil(t, refreshCmd)
	require.True(t, model.fetching)

	model, actionCmd := updateModel(t, model, actionDoneMsg{
		message: "removed kwt:feature",
		refresh: true,
	})
	require.Nil(t, actionCmd)

	backend.rows = []Row{testRow("kwt", "main", "/w/kwt/main")}
	model, queuedRefreshCmd := updateModel(t, model, rowsMsg{rows: backend.rows})

	require.NotNil(t, queuedRefreshCmd)
	assert.True(t, model.fetching)
	before := backend.listCalls
	msg := queuedRefreshCmd()
	assert.Equal(t, before+1, backend.listCalls)
	assert.IsType(t, rowsMsg{}, msg)
}

func TestModelPreservesCreateAnchorAcrossQueuedRefresh(t *testing.T) {
	staleRows := []Row{testRow("kwt", "feature", "/w/kwt/feature")}
	newPath := "/w/kwt/new-feature"
	freshRows := []Row{
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kwt", "new-feature", newPath),
	}
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: staleRows})
	model, _ = updateModel(t, model, press("r"))
	model, _ = updateModel(t, model, actionDoneMsg{
		message:    "created new-feature",
		refresh:    true,
		anchorPath: newPath,
	})

	model, queuedRefreshCmd := updateModel(t, model, rowsMsg{rows: staleRows})
	require.NotNil(t, queuedRefreshCmd)
	require.Equal(t, newPath, model.anchorPath)

	model, _ = updateModel(t, model, rowsMsg{rows: freshRows})

	assert.Equal(t, newPath, rowPath(model.selectedRow()))
	assert.Empty(t, model.anchorPath)
}

func TestModelScrollsRowsWithinTerminalHeight(t *testing.T) {
	rows := make([]Row, 0, 12)
	for i := range 12 {
		branch := fmt.Sprintf("branch-%02d", i)
		rows = append(rows, testRow("kwt", branch, "/w/kwt/"+branch))
	}
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 8})
	model, _ = updateModel(t, model, rowsMsg{rows: rows})
	model, _ = updateModel(t, model, press("G"))

	content := stripANSI(viewContent(model))
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	assert.LessOrEqual(t, len(lines), model.height)
	assert.Contains(t, content, "branch-11")
	assert.NotContains(t, content, "branch-00")
	assert.Contains(t, content, "q quit")
}

func TestModelPathAnchorsCursorAfterRefresh(t *testing.T) {
	model := NewModel(&fakeBackend{}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})
	require.Equal(t, "/w/kwt/feature", rowPath(model.selectedRow()))

	model, _ = updateModel(t, model, rowsMsg{rows: []Row{
		testRow("kata", "main", "/w/kata/main"),
		testRow("kwt", "feature", "/w/kwt/feature"),
		testRow("kwt", "main", "/w/kwt/main"),
	}})

	assert.Equal(t, "/w/kwt/feature", rowPath(model.selectedRow()))
	assert.Equal(t, 1, model.cursor)
}

func TestInitialAnchorSelectsLaunchWorkspaceRow(t *testing.T) {
	active := testRow("kwt", "main", "/w/kwt/main")
	active.Status.LastActivity = time.Now()
	workspace := Row{Workspace: &WorkspaceInfo{Name: "code", Path: "/Users/me/code"}}
	model := NewModel(&fakeBackend{}, "/worktrees").WithInitialAnchor("/Users/me/code")

	model, _ = updateModel(t, model, rowsMsg{rows: []Row{active, workspace}})

	require.NotNil(t, model.selectedRow().Workspace,
		"first load must select the launch directory's workspace row despite activity sorting")
	assert.Equal(t, "/Users/me/code", model.selectedRow().Workspace.Path)

	// The anchor is consumed: a refresh keeps the cursor by path instead of
	// snapping back, and moving afterwards works normally.
	model, _ = updateModel(t, model, press("g"))
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{active, workspace}})
	require.NotNil(t, model.selectedRow().Entry, "anchor must not re-apply on later refreshes")
}

func TestInitialAnchorMissFallsBackToCurrentRow(t *testing.T) {
	other := testRow("kwt", "main", "/w/kwt/main")
	other.Status.LastActivity = time.Now()
	current := testRow("kata", "feature", "/w/kata/feature")
	current.Status.IsCurrent = true
	model := NewModel(&fakeBackend{}, "/worktrees").WithInitialAnchor("/nowhere/unmatched")

	model, _ = updateModel(t, model, rowsMsg{rows: []Row{other, current}})

	require.NotNil(t, model.selectedRow().Entry)
	assert.Equal(t, "/w/kata/feature", model.selectedRow().Entry.Path,
		"an unmatched initial anchor must fall back to the current worktree row")
}
