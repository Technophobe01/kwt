package tmux

import (
	"fmt"
	"strings"
	"time"

	"go.kenn.io/kwt/internal/template"
	"go.kenn.io/kwt/internal/url"
)

type Session struct {
	ID          string            `json:"id"`
	SessionName string            `json:"session_name"`
	Context     string            `json:"context"`
	Identifier  string            `json:"identifier"`
	WorkingDir  string            `json:"working_dir"`
	Command     string            `json:"command"`
	StartTime   time.Time         `json:"start_time"`
	HistorySize int               `json:"history_size"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type SessionOptions struct {
	Context    string
	Identifier string
	WorkingDir string
	Command    string
	Metadata   map[string]string
}

type SessionConfig struct {
	Enabled      bool   `toml:"enabled" json:"enabled"`
	TmuxCommand  string `toml:"tmux_command" json:"tmux_command"`
	HistoryLimit int    `toml:"history_limit" json:"history_limit"`
}

func DefaultSessionConfig() *SessionConfig {
	return &SessionConfig{
		Enabled:      true,
		TmuxCommand:  "tmux",
		HistoryLimit: 50000,
	}
}

// WorkspaceSessionName returns a stable, collision-resistant, tmux-safe session
// name for a worktree workspace: kwt-workspace-{host}-{owner}-{repo}-{branch}-{hash}.
// The hash is computed over worktreePath, which is globally unique per worktree
// (unlike info+branch: two detached-HEAD worktrees of the same repo both report
// branch "HEAD" and would otherwise collide on the same session name).
func WorkspaceSessionName(info *url.RepositoryInfo, branch, worktreePath string) string {
	hash := template.ShortHash(worktreePath)
	raw := fmt.Sprintf("kwt-workspace-%s-%s-%s-%s-%s",
		info.Host, info.Owner, info.Repository, branch, hash)
	return sanitizeTmuxName(raw)
}

// sanitizeTmuxName replaces characters tmux disallows in a session name
// ('.' and ':') plus path separators and whitespace with '-'.
func sanitizeTmuxName(s string) string {
	return strings.NewReplacer(
		".", "-", ":", "-", "/", "-", " ", "-", "\t", "-",
	).Replace(s)
}
