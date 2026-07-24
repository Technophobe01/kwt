package tmux

import (
	"context"
	"fmt"
	"os"
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
	GlobalEnvironment() (string, error)
	SessionEnvironment(session string) (string, error)
}

var _ workspaceTmux = (*TmuxCommand)(nil)

// WorkspaceRunner creates or reuses a tmux workspace session and attaches to it.
type WorkspaceRunner struct {
	tmux            workspaceTmux
	extraStripNames []string
}

// NewWorkspaceRunner returns a runner backed by the given tmux surface.
func NewWorkspaceRunner(t workspaceTmux) *WorkspaceRunner {
	return NewWorkspaceRunnerWithStripNames(t, nil)
}

// NewWorkspaceRunnerWithStripNames installs caller-owned credential removal
// markers in addition to kwt's canonical launcher-state set.
func NewWorkspaceRunnerWithStripNames(
	t workspaceTmux,
	names []string,
) *WorkspaceRunner {
	extra := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			extra = append(extra, name)
		}
	}
	return &WorkspaceRunner{
		tmux:            t,
		extraStripNames: extra,
	}
}

// EnsureAndAttach attaches to the workspace session, creating it first if it
// does not already exist. Creation spawns the first pane, applies the session
// bootstrap, spawns and arranges the remaining panes, then sends each pane's
// command to its captured ID. When the session already exists (e.g. an
// external tool ran new-session first), it re-applies the safe bootstrap
// subset — default-command plus the remove-markers — which is idempotent and
// non-destructive on any session, so a session created bare still gets
// consistent behavior for future windows. insideTmux selects switch-client
// (already in tmux) over attach-session.
func (r *WorkspaceRunner) EnsureAndAttach(
	ctx context.Context, session, worktreeDir string, layout models.Layout, insideTmux bool,
) error {
	if err := r.Ensure(ctx, session, worktreeDir, layout); err != nil {
		return err
	}
	return r.attach(session, insideTmux)
}

// Ensure creates or repairs the workspace session without attaching a client.
// Automation callers can use this to establish kwt's canonical layout and
// bootstrap before handing presentation to another ordinary tmux client.
func (r *WorkspaceRunner) Ensure(
	ctx context.Context, session, worktreeDir string, layout models.Layout,
) error {
	if r.tmux.HasSession(session) {
		if err := r.repairBootstrap(ctx, session); err != nil {
			return err
		}
	} else if err := r.create(ctx, session, worktreeDir, layout); err != nil {
		return err
	}
	return nil
}

// sessionStripNames derives the remove-marker set for session: the
// terminal-agnostic bootstrap kwt applies to every workspace session it
// creates or repairs so launcher-state variables do not leak into panes.
//
// The set is the union of up to four sources so neither a stale server-global
// table nor a stale session-local table can leak variables kwt's own process
// environment happens to lack:
//   - every canonical exact key, unconditionally (a marker for an absent
//     variable is harmless);
//   - the launcher-derived names present in kwt's own environment (this is what
//     picks up prefix-matched variables like VSCODE_* that kwt inherited);
//   - the prefix-matched names read from the server's global environment table,
//     which catches stale variables a pre-existing server holds (e.g. an
//     editor-launched server's VSCODE_*) that kwt's env does not;
//   - only when the session already exists (the repair path), the
//     prefix-matched names read from the session's own environment table,
//     which catches variables a pre-existing session holds ONLY locally —
//     e.g. an editor terminal that created the session captured its VSCODE_*/
//     STARSHIP_* into the session table directly, never promoting them to the
//     server-global table the previous source reads.
//
// A show-environment failure on either query falls back to whichever sources
// remain.
func (r *WorkspaceRunner) sessionStripNames(session string, sessionExists bool) []string {
	launcher := StripEnvNames(os.Environ())
	var serverDerived, sessionDerived []string
	if output, err := r.tmux.GlobalEnvironment(); err == nil {
		serverDerived = CanonicalStripNames(ParseServerEnvNames(output))
	}
	if sessionExists {
		if output, err := r.tmux.SessionEnvironment(session); err == nil {
			sessionDerived = CanonicalStripNames(ParseServerEnvNames(output))
		}
	}
	return MergeStripNames(
		CanonicalStripExactNames(),
		r.extraStripNames,
		launcher,
		serverDerived,
		sessionDerived,
	)
}

// create spawns the first pane as an inert placeholder (it spawns before the
// session exists to carry remove-markers, and a login shell there would run
// profile scripts under the dirty environment and then again after respawn;
// see FirstPanePlaceholderArgv), applies the session bootstrap so the
// strips are in place before any further pane spawns, respawns the first pane
// into the real login shell — its only login-shell spawn, from the stripped
// session environment (see BuildFirstPaneRespawnCommand) — then spawns and
// arranges the remaining panes (capturing one pane ID per pane) and runs the
// pane-command sequence against those IDs. If a command after new-session
// fails, it kills the session it just started rather than leaving a
// half-built workspace — including one whose only pane is still the
// placeholder — behind for a later EnsureAndAttach to find and attach to.
func (r *WorkspaceRunner) create(
	ctx context.Context, session, worktreeDir string, layout models.Layout,
) error {
	// The session does not exist yet, so only the launcher and server-global
	// sources contribute strip names; querying the session table would be a
	// guaranteed-to-fail subprocess.
	stripNames := r.sessionStripNames(session, false)
	createCmd := BuildSessionCreateCommand(session, worktreeDir)
	firstPane, err := r.tmux.RunCommandOutputContext(ctx, createCmd...)
	if err != nil {
		return wrapTmuxErr(createCmd, err)
	}
	paneIDs := make([]string, 0, len(layout.Panes))
	paneIDs = append(paneIDs, strings.TrimSpace(firstPane))

	bootCmd := BuildSessionBootstrapCommand(session, stripNames)
	if err := r.tmux.RunCommandContext(ctx, bootCmd...); err != nil {
		return r.abort(session, bootCmd, err)
	}
	defaultShellCmd := []string{
		"show-options", "-v", "-t", session, "default-shell",
	}
	defaultShellOut, err := r.tmux.RunCommandOutputContext(ctx, defaultShellCmd...)
	if err != nil {
		return r.abort(session, defaultShellCmd, err)
	}
	defaultShell := strings.TrimSpace(defaultShellOut)
	if defaultShell == "" {
		defaultShellCmd = []string{"show-options", "-gv", "default-shell"}
		defaultShellOut, err = r.tmux.RunCommandOutputContext(
			ctx,
			defaultShellCmd...,
		)
		if err != nil {
			return r.abort(session, defaultShellCmd, err)
		}
		defaultShell = strings.TrimSpace(defaultShellOut)
	}
	if defaultShell == "" {
		return r.abort(session, defaultShellCmd, fmt.Errorf("tmux returned an empty default-shell"))
	}
	respawnCmd := BuildFirstPaneRespawnCommand(paneIDs[0], worktreeDir, defaultShell)
	if err := r.tmux.RunCommandContext(ctx, respawnCmd...); err != nil {
		return r.abort(session, respawnCmd, err)
	}
	for _, args := range BuildRemainingPaneSequence(session, worktreeDir, layout) {
		if args[0] == "split-window" {
			out, err := r.tmux.RunCommandOutputContext(ctx, args...)
			if err != nil {
				return r.abort(session, args, err)
			}
			paneIDs = append(paneIDs, strings.TrimSpace(out))
		} else if err := r.tmux.RunCommandContext(ctx, args...); err != nil {
			return r.abort(session, args, err)
		}
	}
	for _, args := range BuildPaneCommandSequence(paneIDs, layout.Panes) {
		if err := r.tmux.RunCommandContext(ctx, args...); err != nil {
			return r.abort(session, args, err)
		}
	}
	return nil
}

// repairBootstrap re-applies the safe bootstrap subset (default-command and
// the session-scoped remove-markers) to an already-existing session. It runs
// no construction or pane commands, so it is non-destructive on any session,
// including one an external tool created bare with new-session. The session is
// not kwt's to tear down, so a failure is wrapped and returned without killing
// it.
func (r *WorkspaceRunner) repairBootstrap(ctx context.Context, session string) error {
	bootCmd := BuildSessionBootstrapCommand(session, r.sessionStripNames(session, true))
	if err := r.tmux.RunCommandContext(ctx, bootCmd...); err != nil {
		return wrapTmuxErr(bootCmd, err)
	}
	return nil
}

// abort wraps a construction-command failure and kills the just-created
// session so it does not linger half-built. Killing is best-effort: a failure
// there is annotated onto the returned error but never replaces it.
func (r *WorkspaceRunner) abort(session string, args []string, err error) error {
	wrapped := wrapTmuxErr(args, err)
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
