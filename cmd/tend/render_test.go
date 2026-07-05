package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
)

func session(id, root, provider, task, status string, ru *api.SessionResourceUsage) api.SessionInfo {
	s := api.SessionInfo{
		SessionID:     api.SessionID(id),
		WorktreeRoot:  root,
		ProviderID:    api.ProviderID(provider),
		Status:        api.SessionStatus(status),
		ResourceUsage: ru,
	}
	if task != "" {
		s.Task = &api.TaskRef{Provider: "beads", ID: task}
	}
	return s
}

func TestRenderTableColumnsAndDegradation(t *testing.T) {
	sessions := []api.SessionInfo{
		session("s1", "/work/tend", "codex", "t1", "running", &api.SessionResourceUsage{CPUPercent: 12.34, RSSBytes: 5 * 1024 * 1024}),
		// Task-less, no resource sample: task/cpu/rss degrade to "-".
		session("s2", "/work/tend", "claude", "", "idle", nil),
	}
	out := renderTable(sessions)

	if !strings.Contains(out, "SESSION") || !strings.Contains(out, "CPU%") || !strings.Contains(out, "RSS") {
		t.Fatalf("missing header:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out)
	}
	// s1: real stats, and the task shows qualified by provider (provider:id).
	if !strings.Contains(out, "tend") || !strings.Contains(out, "beads:t1") || !strings.Contains(out, "12.3") || !strings.Contains(out, "5.0M") {
		t.Errorf("s1 row missing repo/task/cpu/rss:\n%s", out)
	}
	// s2: absent task and stats render as "-", never a fabricated 0.
	s2 := lines[2]
	if !strings.Contains(s2, "s2") || strings.Count(s2, "-") < 3 {
		t.Errorf("s2 row should degrade task/cpu/rss to -:\n%s", s2)
	}
}

func TestRenderTableSortsByRepoThenID(t *testing.T) {
	sessions := []api.SessionInfo{
		session("s9", "/work/zeta", "codex", "", "idle", nil),
		session("s2", "/work/alpha", "codex", "", "idle", nil),
		session("s1", "/work/alpha", "codex", "", "idle", nil),
	}
	out := renderTable(sessions)
	// alpha/s1, alpha/s2, then zeta/s9.
	i1 := strings.Index(out, "s1")
	i2 := strings.Index(out, "s2")
	i9 := strings.Index(out, "s9")
	if i1 >= i2 || i2 >= i9 {
		t.Errorf("order wrong (want s1<s2<s9): %d %d %d\n%s", i1, i2, i9, out)
	}
}

func TestRenderTableEmpty(t *testing.T) {
	if got := renderTable(nil); got != "no sessions\n" {
		t.Errorf("empty render = %q, want \"no sessions\\n\"", got)
	}
}

func TestRenderJSONRoundTrips(t *testing.T) {
	sessions := []api.SessionInfo{session("s1", "/work/tend", "codex", "t1", "running", nil)}
	out, err := renderJSON(sessions)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}
	var got []api.SessionInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "s1" || got[0].Task == nil || got[0].Task.ID != "t1" {
		t.Errorf("round-trip = %+v", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                      "0B",
		512:                    "512B",
		1024:                   "1.0K",
		5 * 1024 * 1024:        "5.0M",
		3 * 1024 * 1024 * 1024: "3.0G",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
