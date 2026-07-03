package tmux

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kwt/pkg/models"
)

// mockWorkspaceTmux implements workspaceTmux, recording every command and
// returning incrementing fake pane IDs ("%1\n", "%2\n", ...) so tests can
// assert the captured-ID -> send-keys wiring without real tmux. outputErr,
// when set, is returned by every RunCommandOutputContext call instead of a
// pane ID, to exercise create's error-propagation path when new-session
// itself (the first such call) fails. failOutputOnCall, when nonzero, instead
// fails only the Nth RunCommandOutputContext call (1-indexed), so tests can
// make a later construction command (e.g. the first split-window) fail after
// new-session already succeeded. failRunOnCall is the same idea for
// RunCommandContext (1-indexed), letting tests fail select-layout or a
// pane-command invocation instead.
type mockWorkspaceTmux struct {
	hasSession        bool
	calls             [][]string
	paneSeq           int
	switchedTo        string
	attachedTo        string
	outputErr         error
	failOutputOnCall  int
	outputCalls       int
	failRunOnCall     int
	runCalls          int
	killSessionCalled bool
	killedSession     string
}

func (m *mockWorkspaceTmux) HasSession(string) bool {
	return m.hasSession
}

func (m *mockWorkspaceTmux) RunCommandContext(_ context.Context, args ...string) error {
	m.calls = append(m.calls, args)
	m.runCalls++
	if m.failRunOnCall != 0 && m.runCalls == m.failRunOnCall {
		return fmt.Errorf("boom on call %d", m.runCalls)
	}
	return nil
}

func (m *mockWorkspaceTmux) RunCommandOutputContext(
	_ context.Context, args ...string,
) (string, error) {
	m.calls = append(m.calls, args)
	m.outputCalls++
	if m.outputErr != nil {
		return "", m.outputErr
	}
	if m.failOutputOnCall != 0 && m.outputCalls == m.failOutputOnCall {
		return "", fmt.Errorf("boom on call %d", m.outputCalls)
	}
	m.paneSeq++
	return fmt.Sprintf("%%%d\n", m.paneSeq), nil
}

func (m *mockWorkspaceTmux) SwitchClient(target string) error {
	m.switchedTo = target
	return nil
}

func (m *mockWorkspaceTmux) AttachSession(session string) error {
	m.attachedTo = session
	return nil
}

func (m *mockWorkspaceTmux) KillSession(session string) error {
	m.killSessionCalled = true
	m.killedSession = session
	return nil
}

// The empty pane sits in the middle (index 1) rather than last: since
// BuildPaneCommandSequence skips empty panes, a trailing empty pane would
// never index its captured ID, letting an off-by-one that drops the last
// pane's capture (i < len(layout.Panes)-1) go unnoticed. With the empty pane
// mid-sequence, "vim" (index 2) must target %3, which only exists if the
// capture boundary includes the third (last) pane-creating command.
func TestEnsureAndAttachCreatesSendsToCapturedIDsAndAttaches(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "even-horizontal", Panes: []string{"codex", "", "vim"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)
	require.NoError(t, err)

	want := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "s", "-c", "/wt"},
		{"split-window", "-P", "-F", "#{pane_id}", "-t", "s", "-c", "/wt"},
		{"split-window", "-P", "-F", "#{pane_id}", "-t", "s", "-c", "/wt"},
		{"select-layout", "-t", "s", "even-horizontal"},
		{"send-keys", "-t", "%1", "-l", "--", "codex"},
		{"send-keys", "-t", "%1", "Enter"},
		{"send-keys", "-t", "%3", "-l", "--", "vim"},
		{"send-keys", "-t", "%3", "Enter"},
		{"select-pane", "-t", "%1"},
	}
	assert.Equal(t, want, m.calls)
	assert.Equal(t, "s", m.attachedTo)
	assert.Empty(t, m.switchedTo)
}

func TestEnsureAndAttachReusesExistingSessionWithoutConstructing(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: true}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "tiled", Panes: []string{"codex"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)
	require.NoError(t, err)

	assert.Empty(t, m.calls, "existing session must not be re-created")
	assert.Equal(t, "s", m.attachedTo)
	assert.Empty(t, m.switchedTo)
}

func TestEnsureAndAttachSwitchesClientInsideTmux(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "tiled", Panes: []string{"codex"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, true)
	require.NoError(t, err)

	assert.Equal(t, "s", m.switchedTo)
	assert.Empty(t, m.attachedTo)
}

func TestEnsureAndAttachReturnsErrorOnCaptureFailure(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false, outputErr: errors.New("boom")}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "tiled", Panes: []string{"codex"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "new-session")
	assert.False(t, m.killSessionCalled,
		"new-session itself failed, so no session was created to kill")
}

// TestEnsureAndAttachKillsPartialSessionOnCreateFailure pins the other side
// of the created boundary: once new-session has succeeded, a later
// construction command failing must kill the now-partially-built session so
// a subsequent EnsureAndAttach rebuilds instead of attaching to it broken.
func TestEnsureAndAttachKillsPartialSessionOnCreateFailure(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false, failOutputOnCall: 2}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "even-horizontal", Panes: []string{"codex", "vim"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "split-window")
	assert.True(t, m.killSessionCalled, "must kill the partially built session")
	assert.Equal(t, "s", m.killedSession)
	assert.Empty(t, m.attachedTo, "must not attach to a killed session")
	assert.Empty(t, m.switchedTo, "must not switch to a killed session")
}

// TestEnsureAndAttachKillsPartialSessionOnLayoutFailure covers cleanup when a
// RunCommandContext call fails at select-layout -- the first such call, which
// runs after new-session and split-window (both RunCommandOutputContext) have
// created the session. The RunCommandOutputContext branch is covered by
// TestEnsureAndAttachKillsPartialSessionOnCreateFailure, and the
// BuildPaneCommandSequence branch by the pane-command test below.
func TestEnsureAndAttachKillsPartialSessionOnLayoutFailure(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false, failRunOnCall: 1}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "even-horizontal", Panes: []string{"codex", "vim"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "select-layout")
	assert.True(t, m.killSessionCalled, "must kill the partially built session")
	assert.Equal(t, "s", m.killedSession)
	assert.Empty(t, m.attachedTo, "must not attach to a killed session")
	assert.Empty(t, m.switchedTo, "must not switch to a killed session")
}

// TestEnsureAndAttachKillsPartialSessionOnPaneCommandFailure covers the last
// cleanup branch: a BuildPaneCommandSequence send-keys failing after the panes
// exist. failRunOnCall 2 targets the first send-keys (call 1 is select-layout).
func TestEnsureAndAttachKillsPartialSessionOnPaneCommandFailure(t *testing.T) {
	m := &mockWorkspaceTmux{hasSession: false, failRunOnCall: 2}
	r := NewWorkspaceRunner(m)
	layout := models.Layout{Arrange: "even-horizontal", Panes: []string{"codex", "vim"}}

	err := r.EnsureAndAttach(context.Background(), "s", "/wt", layout, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "send-keys")
	assert.True(t, m.killSessionCalled, "must kill the partially built session")
	assert.Equal(t, "s", m.killedSession)
	assert.Empty(t, m.attachedTo, "must not attach to a killed session")
	assert.Empty(t, m.switchedTo, "must not switch to a killed session")
}
