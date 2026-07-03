package tui

import (
	"context"

	"go.kenn.io/kwt/internal/discovery"
	"go.kenn.io/kwt/pkg/models"
)

type HandoffKind int

const (
	HandoffNone HandoffKind = iota
	HandoffAttach
	HandoffShell
)

type Handoff struct {
	Kind       HandoffKind
	Row        Row
	LayoutName string
}

type Row struct {
	Entry       *discovery.GlobalWorktreeEntry
	Status      *models.WorktreeStatus
	SessionName string
	SessionLive bool
}

type Backend interface {
	List(ctx context.Context) ([]Row, error)
	CreateWorktree(ctx context.Context, row Row, branch string) (string, error)
	RemoveWorktree(ctx context.Context, row Row) error
	KillSession(row Row) error
	OpenInTmux(ctx context.Context, row Row, layoutName string) error
	LayoutNames() []string
	InsideTmux() bool
}
