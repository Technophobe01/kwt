package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.kenn.io/kwt/pkg/models"
)

var timeNow = time.Now

func rowRepoName(row Row) string {
	if row.Entry != nil && row.Entry.RepositoryInfo != nil && row.Entry.RepositoryInfo.Repository != "" {
		return row.Entry.RepositoryInfo.Repository
	}
	if row.Fleet != nil {
		if row.Fleet.ProjectName != "" {
			return row.Fleet.ProjectName
		}
		if row.Fleet.ProjectIdentity != "" {
			return filepath.Base(row.Fleet.ProjectIdentity)
		}
	}
	if row.Entry != nil && row.Entry.Path != "" {
		return filepath.Base(row.Entry.Path)
	}
	if row.Status != nil && row.Status.Repository != "" {
		return filepath.Base(row.Status.Repository)
	}
	if row.Status != nil && row.Status.Path != "" {
		return filepath.Base(row.Status.Path)
	}
	return ""
}

func rowBranch(row Row) string {
	if row.Entry != nil {
		return row.Entry.Branch
	}
	if row.Fleet != nil {
		if row.Fleet.Branch != "" {
			return row.Fleet.Branch
		}
		return row.Fleet.Ref
	}
	if row.Status != nil {
		return row.Status.Branch
	}
	return ""
}

func rowPath(row Row) string {
	if row.Entry != nil {
		return row.Entry.Path
	}
	if row.Status != nil {
		return row.Status.Path
	}
	return ""
}

func rowLabel(row Row) string {
	return rowRepoName(row) + ":" + rowBranch(row)
}

func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		leftActivity := rowLastActivity(rows[i])
		rightActivity := rowLastActivity(rows[j])
		if !leftActivity.Equal(rightActivity) {
			if leftActivity.IsZero() {
				return false
			}
			if rightActivity.IsZero() {
				return true
			}
			return leftActivity.After(rightActivity)
		}

		leftRepo := strings.ToLower(rowRepoName(rows[i]))
		rightRepo := strings.ToLower(rowRepoName(rows[j]))
		if leftRepo != rightRepo {
			return leftRepo < rightRepo
		}
		leftBranch := strings.ToLower(rowBranch(rows[i]))
		rightBranch := strings.ToLower(rowBranch(rows[j]))
		if leftBranch != rightBranch {
			return leftBranch < rightBranch
		}
		return rowPath(rows[i]) < rowPath(rows[j])
	})
}

func rowLastActivity(row Row) time.Time {
	if row.Status == nil {
		return time.Time{}
	}
	return row.Status.LastActivity
}

func filterRows(rows []Row, filter string) []Row {
	filter = strings.TrimSpace(strings.ToLower(filter))
	if filter == "" {
		return append([]Row(nil), rows...)
	}

	filtered := make([]Row, 0, len(rows))
	for _, row := range rows {
		haystack := strings.ToLower(strings.Join([]string{
			rowRepoName(row),
			rowBranch(row),
			rowPath(row),
			rowLabel(row),
			rowFleetHaystack(row),
		}, " "))
		if strings.Contains(haystack, filter) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func filterProjectRows(rows []Row, filter string) []Row {
	filter = strings.TrimSpace(strings.ToLower(filter))
	if filter == "" {
		return append([]Row(nil), rows...)
	}

	filtered := make([]Row, 0, len(rows))
	for _, row := range rows {
		haystack := strings.ToLower(strings.Join(rowProjectIdentity(row), " "))
		if strings.Contains(haystack, filter) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func filterProjectPerspectiveRows(rows []Row, project string) []Row {
	project = strings.TrimSpace(project)
	if project == "" {
		return append([]Row(nil), rows...)
	}

	filtered := make([]Row, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(rowProjectKey(row), project) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func rowProjectIdentity(row Row) []string {
	parts := []string{rowRepoName(row)}
	if row.Entry != nil && row.Entry.RepositoryInfo != nil {
		info := row.Entry.RepositoryInfo
		parts = append(parts, info.Host, info.Owner, info.Repository, info.FullPath)
	}
	if row.Status != nil {
		parts = append(parts, row.Status.Repository)
	}
	if row.Fleet != nil {
		parts = append(parts,
			row.Fleet.ProjectIdentity,
			row.Fleet.ProjectName,
			strings.Join(row.Fleet.Hosts, " "),
		)
	}
	return parts
}

func rowProjectKey(row Row) string {
	if row.Entry != nil && row.Entry.RepositoryInfo != nil {
		info := row.Entry.RepositoryInfo
		if info.FullPath != "" {
			return info.FullPath
		}
		if info.Host != "" && info.Owner != "" && info.Repository != "" {
			return path.Join(info.Host, info.Owner, info.Repository)
		}
	}
	if row.Status != nil && row.Status.Repository != "" {
		return row.Status.Repository
	}
	if row.Fleet != nil {
		if row.Fleet.ProjectIdentity != "" {
			return row.Fleet.ProjectIdentity
		}
		if row.Fleet.ProjectName != "" {
			return row.Fleet.ProjectName
		}
	}
	return rowRepoName(row)
}

func formatChanges(status *models.WorktreeStatus) string {
	if status == nil || status.Status == models.WorktreeStatusUnknown {
		return "?"
	}
	gitStatus := status.GitStatus
	added := gitStatus.Added + gitStatus.Staged
	if added == 0 &&
		gitStatus.Modified == 0 &&
		gitStatus.Deleted == 0 &&
		gitStatus.Untracked == 0 &&
		gitStatus.Conflicts == 0 {
		return "clean"
	}

	parts := make([]string, 0, 5)
	if added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", added))
	}
	if gitStatus.Modified > 0 {
		parts = append(parts, fmt.Sprintf("~%d", gitStatus.Modified))
	}
	if gitStatus.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("-%d", gitStatus.Deleted))
	}
	if gitStatus.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("?%d", gitStatus.Untracked))
	}
	if gitStatus.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("!%d", gitStatus.Conflicts))
	}
	return strings.Join(parts, " ")
}

func formatSync(status *models.WorktreeStatus) string {
	if status == nil || status.Status == models.WorktreeStatusUnknown {
		return "?"
	}
	return fmt.Sprintf("↑%d ↓%d", status.GitStatus.Ahead, status.GitStatus.Behind)
}

func formatRowChanges(row Row) string {
	if row.Fleet != nil && row.Fleet.Dirty != "" {
		if row.Status == nil || row.Fleet.Dirty != "clean" {
			return row.Fleet.Dirty
		}
	}
	return formatChanges(row.Status)
}

func formatRowSync(row Row) string {
	if row.Fleet != nil && row.Fleet.Sync != "" {
		return "hosts " + row.Fleet.Sync
	}
	return "git " + formatSync(row.Status)
}

func formatMachines(row Row) string {
	if row.Fleet == nil {
		return "local"
	}
	hosts := append([]string(nil), row.Fleet.Hosts...)
	if len(hosts) == 0 {
		if row.Fleet.Local {
			return "local"
		}
		return "remote"
	}
	if !row.Fleet.Local {
		if len(hosts) == 1 {
			return hosts[0] + " only"
		}
		return strings.Join(hosts, ", ")
	}
	if len(hosts) == 1 && containsHost(hosts, "local") {
		return "local"
	}
	return strings.Join(hosts, ", ")
}

func formatRowActivity(row Row, now time.Time) string {
	if row.Status != nil {
		return formatActivityAt(row.Status.LastActivity, now)
	}
	if row.Fleet != nil && row.Fleet.Freshness != "" {
		return row.Fleet.Freshness
	}
	return "-"
}

func containsHost(hosts []string, want string) bool {
	for _, host := range hosts {
		if host == want {
			return true
		}
	}
	return false
}

func rowFleetHaystack(row Row) string {
	if row.Fleet == nil {
		return ""
	}
	return strings.Join([]string{
		row.Fleet.ProjectIdentity,
		row.Fleet.ProjectName,
		row.Fleet.Ref,
		row.Fleet.Branch,
		strings.Join(row.Fleet.Hosts, " "),
		row.Fleet.Sync,
		row.Fleet.Dirty,
		row.Fleet.RemotePath,
	}, " ")
}

func formatActivityAt(activity, now time.Time) string {
	if activity.IsZero() {
		return "-"
	}
	if activity.After(now) {
		return "now"
	}

	elapsed := now.Sub(activity)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed/time.Hour))
	case elapsed < 21*24*time.Hour:
		return fmt.Sprintf("%dd", int(elapsed/(24*time.Hour)))
	default:
		return fmt.Sprintf("%dw", int(elapsed/(7*24*time.Hour)))
	}
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

type tableColumn struct {
	header string
	width  int
}

var dashboardColumns = []tableColumn{
	{header: "REPO", width: 14},
	{header: "BRANCH", width: 22},
	{header: "MACHINES", width: 20},
	{header: "CHANGES", width: 11},
	{header: "HEADS", width: 22},
	{header: "ACTIVITY", width: 10},
	{header: "WORKSPACE"},
}

func renderDashboardCells(values []string) string {
	var b strings.Builder
	for i, value := range values {
		if i > 0 {
			b.WriteByte(' ')
		}
		width := dashboardColumns[i].width
		if width > 0 {
			_, _ = fmt.Fprintf(&b, "%-*s", width, truncateWithEllipsis(value, width))
		} else {
			b.WriteString(value)
		}
	}
	return b.String()
}

func renderDashboardHeader() string {
	values := make([]string, 0, len(dashboardColumns))
	for _, column := range dashboardColumns {
		values = append(values, column.header)
	}
	return "  " + renderDashboardCells(values)
}

func anchorCursorByPath(oldRows []Row, oldCursor int, newRows []Row) int {
	if len(newRows) == 0 {
		return 0
	}
	if oldCursor < 0 || oldCursor >= len(oldRows) {
		return 0
	}
	path := rowPath(oldRows[oldCursor])
	if path == "" {
		return clampCursor(oldCursor, len(newRows))
	}
	for i, row := range newRows {
		if rowPath(row) == path {
			return i
		}
	}
	return clampCursor(oldCursor, len(newRows))
}

func indexByPath(rows []Row, path string) int {
	index, _ := indexByPathOK(rows, path)
	return index
}

func indexByPathOK(rows []Row, path string) (int, bool) {
	for i, row := range rows {
		if rowPath(row) == path {
			return i, true
		}
	}
	return 0, false
}

func clampCursor(cursor, length int) int {
	if length <= 0 || cursor < 0 {
		return 0
	}
	if cursor >= length {
		return length - 1
	}
	return cursor
}

func truncateWithEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	return string(runes[:width-3]) + "..."
}

func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
