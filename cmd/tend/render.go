package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/memimport"
)

// renderJSON emits v as indented JSON for scripting, so any field is available.
func renderJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// renderImport formats an import run as an aligned table plus a summary line.
func renderImport(res memimport.Result, dryRun bool) string {
	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}
	if len(res.Outcomes) == 0 {
		return prefix + "no importable memory files found\n"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tSOURCE\tID\tORIGIN\tNOTE")
	for _, o := range res.Outcomes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", o.Status, o.Source, o.ID, o.Origin, o.Reason)
	}
	_ = w.Flush()

	c := res.Counts()
	_, _ = fmt.Fprintf(&b, "%s%d created, %d updated, %d unchanged, %d skipped\n",
		prefix, c[memimport.StatusCreated], c[memimport.StatusUpdated],
		c[memimport.StatusUnchanged], c[memimport.StatusSkipped])
	return b.String()
}

// renderTable formats the sessions as a plain aligned table, ordered by
// repo then session id. A stat the daemon does not report (no cpu/mem sample, a
// task-less session) shows as "-" rather than a fabricated value.
func renderTable(sessions []api.SessionInfo) string {
	if len(sessions) == 0 {
		return "no sessions\n"
	}
	rows := append([]api.SessionInfo(nil), sessions...)
	sort.Slice(rows, func(i, j int) bool {
		if ri, rj := repoName(rows[i]), repoName(rows[j]); ri != rj {
			return ri < rj
		}
		return rows[i].SessionID < rows[j].SessionID
	})

	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SESSION\tREPO\tPROVIDER\tTASK\tSTATUS\tCPU%\tRSS")
	for _, s := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.SessionID, repoName(s), s.ProviderID, taskRef(s), s.Status, cpu(s), rss(s))
	}
	_ = w.Flush()
	return b.String()
}

// repoName is a short, human label for the session's workspace: the base name of
// its worktree root (the workspace id is the git common dir, which is less
// readable).
func repoName(s api.SessionInfo) string {
	if s.WorktreeRoot == "" {
		return string(s.WorkspaceID)
	}
	return filepath.Base(s.WorktreeRoot)
}

// taskRef renders a session's task as provider:id. ps is global across
// workspaces and providers, so a bare id could collide across task sources; the
// provider qualifies it (the REPO column carries the workspace).
func taskRef(s api.SessionInfo) string {
	if s.Task == nil || s.Task.ID == "" {
		return "-"
	}
	if s.Task.Provider == "" {
		return s.Task.ID
	}
	return s.Task.Provider + ":" + s.Task.ID
}

func cpu(s api.SessionInfo) string {
	if s.ResourceUsage == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", s.ResourceUsage.CPUPercent)
}

func rss(s api.SessionInfo) string {
	if s.ResourceUsage == nil {
		return "-"
	}
	return humanBytes(s.ResourceUsage.RSSBytes)
}

// humanBytes formats a byte count with a binary-unit suffix (K/M/G...), one
// decimal place above bytes.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
