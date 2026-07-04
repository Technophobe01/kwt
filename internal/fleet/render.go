package fleet

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"go.kenn.io/kwt/internal/table"
)

// StatusRow is one rendered fleet status row.
type StatusRow struct {
	Project   string
	Ref       string
	Local     string
	Hosts     string
	Sync      string
	Dirty     string
	Freshness string
}

// BuildStatusRows converts fleet state into user-facing status rows.
func BuildStatusRows(state FleetState, currentHost string, now time.Time) []StatusRow {
	rows := make([]StatusRow, 0, len(state.Rows))
	for _, row := range state.Rows {
		rows = append(rows, StatusRow{
			Project:   renderProject(row),
			Ref:       renderRef(row),
			Local:     renderLocal(row.Observations, currentHost),
			Hosts:     renderHosts(row.Observations),
			Sync:      renderSync(row.Observations, currentHost, now),
			Dirty:     renderDirty(row.Observations),
			Freshness: renderFreshness(row.Observations, now),
		})
	}
	return rows
}

// RenderStatusTable writes fleet status rows as a table.
func RenderStatusTable(w io.Writer, rows []StatusRow) error {
	if len(rows) == 0 {
		return nil
	}

	t := table.New().
		SetOutput(w).
		Headers("PROJECT", "REF", "LOCAL", "HOSTS", "SYNC", "DIRTY", "FRESHNESS")
	for _, row := range rows {
		t.Row(row.Project, row.Ref, row.Local, row.Hosts, row.Sync, row.Dirty, row.Freshness)
	}
	return t.Println()
}

func renderProject(row FleetRow) string {
	if name := strings.TrimSpace(row.ProjectName); name != "" {
		return name
	}
	return strings.TrimSpace(row.ProjectIdentity)
}

func renderRef(row FleetRow) string {
	if branch := strings.TrimSpace(row.Branch); branch != "" {
		return branch
	}
	return strings.TrimSpace(row.Ref)
}

func renderLocal(observations []Observation, currentHost string) string {
	currentHost = strings.TrimSpace(currentHost)
	for _, observation := range observations {
		if observation.HostID == currentHost {
			return "materialized"
		}
	}
	return "missing"
}

func renderHosts(observations []Observation) string {
	hosts := uniqueHosts(observations)
	if len(hosts) == 0 {
		return "-"
	}
	return strings.Join(hosts, ", ")
}

func renderSync(observations []Observation, currentHost string, now time.Time) string {
	if len(observations) <= 1 {
		return "same"
	}

	baseHead, ok := baseHead(observations, currentHost)
	if !ok {
		return "same"
	}
	for _, observation := range observations {
		if observation.Head != baseHead {
			return fmt.Sprintf("differs from %s (%s)", observation.HostID, renderAge(observation.ObservedAt, now))
		}
	}
	return "same"
}

func baseHead(observations []Observation, currentHost string) (string, bool) {
	currentHost = strings.TrimSpace(currentHost)
	if currentHost != "" {
		for _, observation := range observations {
			if observation.HostID == currentHost {
				return observation.Head, true
			}
		}
	}
	if len(observations) == 0 {
		return "", false
	}
	return observations[0].Head, true
}

func renderDirty(observations []Observation) string {
	dirty := make([]string, 0, len(observations))
	for _, observation := range observations {
		summary := renderChangeStatus(observation.Status)
		if summary == "" {
			continue
		}
		dirty = append(dirty, fmt.Sprintf("%s (%s)", observation.HostID, summary))
	}
	if len(dirty) == 0 {
		return "clean"
	}
	sort.Strings(dirty)
	return strings.Join(dirty, ", ")
}

func renderChangeStatus(status ChangeStatus) string {
	parts := make([]string, 0, 6)
	if status.Staged > 0 {
		parts = append(parts, formatCount(status.Staged, "staged"))
	}
	if status.Modified > 0 {
		parts = append(parts, formatCount(status.Modified, "modified"))
	}
	if status.Added > 0 {
		parts = append(parts, formatCount(status.Added, "added"))
	}
	if status.Deleted > 0 {
		parts = append(parts, formatCount(status.Deleted, "deleted"))
	}
	if status.Untracked > 0 {
		parts = append(parts, formatCount(status.Untracked, "untracked"))
	}
	if status.Conflicts > 0 {
		parts = append(parts, formatCount(status.Conflicts, "conflict"))
	}
	return strings.Join(parts, ", ")
}

func formatCount(count int, label string) string {
	if label == "conflict" && count != 1 {
		return fmt.Sprintf("%d conflicts", count)
	}
	return fmt.Sprintf("%d %s", count, label)
}

func renderFreshness(observations []Observation, now time.Time) string {
	newest, ok := newestObservation(observations)
	if !ok {
		return "unknown"
	}
	return renderAge(newest, now)
}

func newestObservation(observations []Observation) (time.Time, bool) {
	var newest time.Time
	for _, observation := range observations {
		if observation.ObservedAt.IsZero() {
			continue
		}
		if newest.IsZero() || observation.ObservedAt.After(newest) {
			newest = observation.ObservedAt
		}
	}
	if newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}

func renderAge(observedAt time.Time, now time.Time) string {
	if observedAt.IsZero() {
		return "unknown"
	}
	if now.IsZero() {
		now = time.Now()
	}
	duration := now.Sub(observedAt)
	if duration < 0 {
		duration = 0
	}
	switch {
	case duration < time.Minute:
		return "just now"
	case duration < time.Hour:
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", minutes)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case duration < 7*24*time.Hour:
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		weeks := int(duration.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}
}

func uniqueHosts(observations []Observation) []string {
	seen := map[string]struct{}{}
	for _, observation := range observations {
		hostID := strings.TrimSpace(observation.HostID)
		if hostID == "" {
			continue
		}
		seen[hostID] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for hostID := range seen {
		hosts = append(hosts, hostID)
	}
	sort.Strings(hosts)
	return hosts
}
