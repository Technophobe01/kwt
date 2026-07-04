package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	killCalls       []string
	openCalls       []string
}

func (b *fakeBackend) List(ctx context.Context) ([]Row, error) {
	b.listCalls++
	return append([]Row(nil), b.rows...), nil
}

func (b *fakeBackend) CreateWorktree(ctx context.Context, row Row, branch string) (string, error) {
	b.createCalls = append(b.createCalls, rowPath(row)+":"+branch)
	return b.createPath, b.createErr
}

func (b *fakeBackend) RemoveWorktree(ctx context.Context, row Row) error {
	b.removeCalls = append(b.removeCalls, rowPath(row))
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

func TestRenderHelpTableReflowsToFitWidth(t *testing.T) {
	got := stripANSI(renderHelpTable(defaultHelpRows(), 34))
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
	assert.Contains(t, content, "layout default")
	assert.Contains(t, content, "selected kwt:test/layouts")
	assert.Contains(t, content, "/worktrees/github.com/example/kwt/test-layouts")
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

	assert.Contains(t, content, "MACHINES")
	assert.Contains(t, content, "kwt")
	assert.Contains(t, content, "feature/studio-only")
	assert.Contains(t, content, "host-b only")
	assert.Contains(t, content, "hosts same")
	assert.Contains(t, content, "remote")
	assert.Contains(t, content, "m materialize")
	assert.Contains(t, content, "selected kwt:feature/studio-only")
	assert.Contains(t, content, "remote on host-b")
	assert.Contains(t, content, "press m to materialize (branch must be pushed/fetched here)")
	assert.Contains(t, content, "source is 2 commits ahead of origin/feature/studio-only")
	assert.Contains(t, content, "/work/host-b/kwt/feature-studio-only")
}

func TestModelCyclesLayoutSelection(t *testing.T) {
	model := NewModel(&fakeBackend{layoutNames: []string{"quad", "focus"}}, "/worktrees")
	model, _ = updateModel(t, model, rowsMsg{rows: []Row{testRow("kwt", "main", "/w/kwt/main")}})

	assert.Contains(t, viewContent(model), "layout default")

	model, _ = updateModel(t, model, press("L"))
	assert.Equal(t, "quad", model.selectedLayout)
	assert.Contains(t, viewContent(model), "layout quad")

	model, _ = updateModel(t, model, press("L"))
	assert.Equal(t, "focus", model.selectedLayout)
	assert.Contains(t, viewContent(model), "layout focus")

	model, _ = updateModel(t, model, press("L"))
	assert.Empty(t, model.selectedLayout)
	assert.Contains(t, viewContent(model), "layout default")
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

	model, cmd := updateModel(t, model, press("m"))

	require.NotNil(t, cmd)
	assert.Contains(t, model.message, "materializing kwt:feature/studio-only")
	msg := cmd()
	assert.IsType(t, actionDoneMsg{}, msg)
	assert.Equal(t, []string{"github.com/example/kwt:feature/studio-only"}, backend.materializeRows)
	done := msg.(actionDoneMsg)
	assert.True(t, done.refresh)
	assert.Equal(t, "/worktrees/github.com/example/kwt/feature-studio-only", done.anchorPath)
	assert.Contains(t, done.message, "materialized kwt:feature/studio-only")
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

	model, _ = updateModel(t, model, press("s"))
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
