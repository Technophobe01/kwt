package tmux

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/kwt/pkg/models"
)

// workspaceTmux is the minimal tmux surface the workspace runner needs.
// *TmuxCommand satisfies it; tests use a mock.
type workspaceTmux interface {
	HasSession(session string) bool
	RunCommandContext(ctx context.Context, args ...string) error
	RunCommandOutputContext(ctx context.Context, args ...string) (string, error)
	SwitchClient(target string) error
	AttachSession(session string) error
	KillSession(session string) error
}

var _ workspaceTmux = (*TmuxCommand)(nil)

// WorkspaceRunner creates or reuses a tmux workspace session and attaches to it.
type WorkspaceRunner struct {
	tmux workspaceTmux
}

// NewWorkspaceRunner returns a runner backed by the given tmux surface.
func NewWorkspaceRunner(t workspaceTmux) *WorkspaceRunner {
	return &WorkspaceRunner{tmux: t}
}

// EnsureAndAttach attaches to the workspace session, creating it first if it
// does not already exist. Creation builds all panes (capturing their stable
// pane IDs), arranges them, then sends each pane's command to its captured ID.
// insideTmux selects switch-client (already in tmux) over attach-session.
func (r *WorkspaceRunner) EnsureAndAttach(
	ctx context.Context, session, worktreeDir string, layout models.Layout, insideTmux bool,
) error {
	if !r.tmux.HasSession(session) {
		if err := r.create(ctx, session, worktreeDir, layout); err != nil {
			return err
		}
	}
	return r.attach(session, insideTmux)
}

// create runs the construction sequence (capturing one pane ID per pane) and
// then the pane-command sequence against those IDs. If a command after
// new-session fails, it kills the session it just started rather than
// leaving a half-built workspace behind for a later EnsureAndAttach to find
// and attach to.
func (r *WorkspaceRunner) create(
	ctx context.Context, session, worktreeDir string, layout models.Layout,
) error {
	construction := BuildConstructionSequence(session, worktreeDir, layout)
	paneIDs := make([]string, 0, len(layout.Panes))
	created := false
	for i, args := range construction {
		// The first len(Panes) commands (new-session + splits) each print a
		// pane ID; the trailing select-layout prints nothing.
		if i < len(layout.Panes) {
			out, err := r.tmux.RunCommandOutputContext(ctx, args...)
			if err != nil {
				return r.abort(created, session, args, err)
			}
			created = true // new-session (the first such command) has now run
			paneIDs = append(paneIDs, strings.TrimSpace(out))
		} else if err := r.tmux.RunCommandContext(ctx, args...); err != nil {
			return r.abort(created, session, args, err)
		}
	}
	for _, args := range BuildPaneCommandSequence(paneIDs, layout.Panes) {
		if err := r.tmux.RunCommandContext(ctx, args...); err != nil {
			return r.abort(created, session, args, err)
		}
	}
	return nil
}

// abort wraps a construction-command failure and, if the session was already
// created, kills it so it does not linger half-built. Killing is best-effort:
// a failure there is annotated onto the returned error but never replaces it.
func (r *WorkspaceRunner) abort(created bool, session string, args []string, err error) error {
	wrapped := wrapTmuxErr(args, err)
	if !created {
		return wrapped
	}
	if killErr := r.tmux.KillSession(session); killErr != nil {
		return fmt.Errorf("%w (also failed to kill partial session: %v)", wrapped, killErr)
	}
	return wrapped
}

// wrapTmuxErr annotates a failed tmux invocation with the command that failed.
func wrapTmuxErr(args []string, err error) error {
	return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
}

// attach connects the current client to the session.
func (r *WorkspaceRunner) attach(session string, insideTmux bool) error {
	if insideTmux {
		return r.tmux.SwitchClient(session)
	}
	return r.tmux.AttachSession(session)
}
