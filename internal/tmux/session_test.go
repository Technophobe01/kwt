package tmux

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/kwt/internal/url"
)

func TestDefaultSessionConfig(t *testing.T) {
	config := DefaultSessionConfig()

	if !config.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if config.TmuxCommand != "tmux" {
		t.Errorf("Expected TmuxCommand to be 'tmux', got '%s'", config.TmuxCommand)
	}

	if config.HistoryLimit != 50000 {
		t.Errorf("Expected HistoryLimit to be 50000, got %d", config.HistoryLimit)
	}
}

func TestSessionOptionsCreation(t *testing.T) {
	opts := SessionOptions{
		Context:    "test",
		Identifier: "test-session",
		WorkingDir: "/tmp",
		Command:    "echo hello",
		Metadata: map[string]string{
			"created_by": "test",
		},
	}

	if opts.Context != "test" {
		t.Errorf("Expected Context to be 'test', got '%s'", opts.Context)
	}

	if opts.Identifier != "test-session" {
		t.Errorf("Expected Identifier to be 'test-session', got '%s'", opts.Identifier)
	}

	if opts.Command != "echo hello" {
		t.Errorf("Expected Command to be 'echo hello', got '%s'", opts.Command)
	}
}

func TestWorkspaceSessionName(t *testing.T) {
	info := &url.RepositoryInfo{
		Host: "github.com", Owner: "wesm", Repository: "kwt",
		FullPath: "github.com/wesm/kwt",
	}
	worktreePath := "/home/u/worktrees/github.com/wesm/kwt/feature/foo"
	name := WorkspaceSessionName(info, "feature/foo", worktreePath)

	// Golden value pins field order, the worktreePath hash basis, and
	// sanitization at once — a swapped/dropped field or a narrowed hash input
	// trips this where the structural checks below would not.
	assert.Equal(t, "kwt-workspace-github-com-wesm-kwt-feature-foo-9cc4e551", name)

	assert.True(t, strings.HasPrefix(name, "kwt-workspace-"), "must carry the kwt- prefix")
	assert.NotContains(t, name, ".", "tmux names cannot contain '.'")
	assert.NotContains(t, name, ":", "tmux names cannot contain ':'")
	assert.NotContains(t, name, "/", "slashes must be sanitized")
	assert.Contains(t, name, "github-com")

	// Stable across calls for the same (info, branch, worktreePath).
	assert.Equal(t, name, WorkspaceSessionName(info, "feature/foo", worktreePath))

	// Two detached-HEAD worktrees of the same repo share info and branch
	// "HEAD" (git reports "HEAD" for any detached worktree), but have distinct
	// worktree paths — they must not collide on the same session name.
	detached1 := WorkspaceSessionName(info, "HEAD", "/home/u/worktrees/github.com/wesm/kwt/wt1")
	detached2 := WorkspaceSessionName(info, "HEAD", "/home/u/worktrees/github.com/wesm/kwt/wt2")
	assert.NotEqual(t, detached1, detached2)
}
