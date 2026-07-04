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
	Fleet       *FleetInfo
	SessionName string
	SessionLive bool
}

// FleetInfo is the TUI-facing summary of one multi-machine sync row.
type FleetInfo struct {
	ProjectIdentity  string
	ProjectName      string
	Kind             string
	Ref              string
	Branch           string
	Local            bool
	Hosts            []string
	Sync             string
	Dirty            string
	Freshness        string
	MaterializeHost  string
	RemotePath       string
	RemoteHead       string
	RemoteUpstream   string
	RemoteAhead      int
	CanMaterialize   bool
	MaterializeLabel string
}

type Backend interface {
	List(ctx context.Context) ([]Row, error)
	CreateWorktree(ctx context.Context, row Row, branch string) (string, error)
	MaterializeWorktree(ctx context.Context, row Row) (string, error)
	RemoveWorktree(ctx context.Context, row Row) error
	KillSession(row Row) error
	OpenInTmux(ctx context.Context, row Row, layoutName string) error
	LayoutNames() []string
	InsideTmux() bool
}
