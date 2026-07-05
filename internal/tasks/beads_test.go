package tasks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// Beads must satisfy the Provider interface.
var _ Provider = (*Beads)(nil)

// fakeBd records the bd invocations and returns canned stdout keyed by subcommand.
type fakeBd struct {
	responses map[string]string
	calls     [][]string
}

func (f *fakeBd) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if out, ok := f.responses[args[0]]; ok {
		return []byte(out), nil
	}
	return nil, nil
}

func newBeadsFake(responses map[string]string) (*Beads, *fakeBd) {
	f := &fakeBd{responses: responses}
	b := NewBeads("beads", "ws1", "/repo")
	b.run = f.run
	return b, f
}

func lastCall(t *testing.T, f *fakeBd) []string {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("no bd calls recorded")
	}
	return f.calls[len(f.calls)-1]
}

func hasArgs(args []string, want ...string) bool {
	for _, w := range want {
		if !slices.Contains(args, w) {
			return false
		}
	}
	return true
}

func TestBeadsCreate(t *testing.T) {
	b, f := newBeadsFake(map[string]string{
		"create": `{"id":"ws1-abc","title":"fix","description":"d","status":"open"}`,
	})
	tk, err := b.Create(context.Background(), CreateParams{Title: "fix", Description: "d", Labels: []string{"m0", "ui"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tk.Ref.Provider != "beads" || tk.Ref.WorkspaceID != "ws1" || tk.Ref.ID != "ws1-abc" {
		t.Fatalf("ref = %+v", tk.Ref)
	}
	if tk.Status != StatusOpen || tk.Title != "fix" {
		t.Errorf("task = %+v", tk)
	}
	// Labels not echoed by bd fall back to the requested ones.
	if len(tk.Labels) != 2 || tk.Labels[0] != "m0" {
		t.Errorf("labels = %v", tk.Labels)
	}
	args := lastCall(t, f)
	if args[0] != "create" || !hasArgs(args, "fix", "--json", "-d", "d", "-l", "m0,ui") {
		t.Errorf("create args = %v", args)
	}
}

func TestBeadsShow(t *testing.T) {
	b, f := newBeadsFake(map[string]string{
		"show": `[{"id":"ws1-abc","title":"fix","status":"in_progress","description":"d","assignee":"alice","labels":["m0"],"comments":[{"author":"bob","text":"hi","created_at":"2026-06-07T00:00:00Z"}]}]`,
	})
	tk, err := b.Show(context.Background(), b.ref("ws1-abc"))
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if tk.Status != StatusInProgress || tk.Assignee != "alice" || len(tk.Labels) != 1 {
		t.Errorf("task = %+v", tk)
	}
	if len(tk.Comments) != 1 || tk.Comments[0].Author != "bob" || tk.Comments[0].Text != "hi" {
		t.Errorf("comments = %+v", tk.Comments)
	}
	if args := lastCall(t, f); !hasArgs(args, "show", "ws1-abc", "--long", "--json") {
		t.Errorf("show args = %v", args)
	}
}

func TestBeadsShowMissing(t *testing.T) {
	b, _ := newBeadsFake(map[string]string{"show": `[]`})
	if _, err := b.Show(context.Background(), b.ref("nope")); err == nil {
		t.Error("Show of a missing task should error")
	}
}

func TestBeadsListFilter(t *testing.T) {
	b, f := newBeadsFake(map[string]string{
		"list": `[{"id":"ws1-a","title":"x","status":"open"},{"id":"ws1-b","title":"y","status":"open"}]`,
	})
	tasks, err := b.List(context.Background(), Filter{Status: StatusOpen})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 || tasks[0].Ref.ID != "ws1-a" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if args := lastCall(t, f); !hasArgs(args, "list", "--json", "--status", "open") {
		t.Errorf("list args = %v", args)
	}
}

func TestBeadsListAppliesSourceLabels(t *testing.T) {
	// A source scoped to a label subset filters every List to that subset, so
	// one planning repo can back several code repos that each see only their slice.
	f := &fakeBd{responses: map[string]string{"list": `[{"id":"p-a","title":"x","status":"open"}]`}}
	b := NewBeads("tend", "ws1", "/planning", "repo:tend", "team:core")
	b.run = f.run
	if _, err := b.List(context.Background(), Filter{Status: StatusOpen}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if args := lastCall(t, f); !hasArgs(args, "list", "--json", "--status", "open", "-l", "repo:tend,team:core") {
		t.Errorf("list args = %v", args)
	}
}

func TestBeadsCreateAppliesSourceLabels(t *testing.T) {
	// Writing through a filtered source tags the task with the source's labels
	// (unioned with any requested), so it lands in that source's view.
	f := &fakeBd{responses: map[string]string{"create": `{"id":"p-a","title":"x","status":"open"}`}}
	b := NewBeads("tend", "ws1", "/planning", "repo:tend")
	b.run = f.run
	tk, err := b.Create(context.Background(), CreateParams{Title: "x", Labels: []string{"bug", "repo:tend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Source label is present, requested labels kept, no duplicate of repo:tend.
	if got := lastCall(t, f); !hasArgs(got, "-l", "bug,repo:tend") {
		t.Errorf("create args = %v", got)
	}
	if !slices.Equal(tk.Labels, []string{"bug", "repo:tend"}) {
		t.Errorf("task labels = %v, want [bug repo:tend]", tk.Labels)
	}
}

func TestBeadsClaim(t *testing.T) {
	b, f := newBeadsFake(map[string]string{"update": ""})
	if err := b.Claim(context.Background(), b.ref("ws1-a"), "alice"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	args := lastCall(t, f)
	if !hasArgs(args, "update", "ws1-a", "--assignee", "alice", "--status", "in_progress") {
		t.Errorf("claim args = %v", args)
	}
}

func TestBeadsCommentAndClose(t *testing.T) {
	b, f := newBeadsFake(map[string]string{"comment": "", "close": ""})
	if err := b.Comment(context.Background(), b.ref("ws1-a"), Comment{Author: "alice", Text: "note"}); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if args := lastCall(t, f); !hasArgs(args, "comment", "ws1-a", "note", "--actor", "alice") {
		t.Errorf("comment args = %v", args)
	}
	if err := b.Close(context.Background(), b.ref("ws1-a")); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if args := lastCall(t, f); !hasArgs(args, "close", "ws1-a") {
		t.Errorf("close args = %v", args)
	}
}

func TestBeadsLinkTypes(t *testing.T) {
	b, f := newBeadsFake(map[string]string{"link": ""})
	// depends_on uses bd's default (no --type).
	if err := b.Link(context.Background(), b.ref("a"), b.ref("c"), LinkDependsOn); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if args := lastCall(t, f); hasArgs(args, "--type") {
		t.Errorf("depends_on should not pass --type: %v", args)
	}
	if err := b.Link(context.Background(), b.ref("a"), b.ref("c"), LinkParent); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if args := lastCall(t, f); !hasArgs(args, "link", "a", "c", "--type", "parent-child") {
		t.Errorf("parent link args = %v", args)
	}
}

func TestBeadsRejectsForeignRef(t *testing.T) {
	b, _ := newBeadsFake(nil)
	foreign := api.TaskRef{Provider: "fake", WorkspaceID: "ws1", ID: "a"}
	if _, err := b.Show(context.Background(), foreign); err == nil {
		t.Error("Show of a foreign-provider ref should be rejected")
	}
	otherWS := api.TaskRef{Provider: "beads", WorkspaceID: "ws2", ID: "a"}
	if err := b.Close(context.Background(), otherWS); err == nil {
		t.Error("Close of an other-workspace ref should be rejected")
	}
}

func TestBeadsEmitsEvents(t *testing.T) {
	b, _ := newBeadsFake(map[string]string{
		"create": `{"id":"ws1-a","title":"x","status":"open"}`,
		"close":  "",
	})
	ctx := t.Context()
	ch, _ := b.Events(ctx)

	tk, _ := b.Create(ctx, CreateParams{Title: "x"})
	_ = b.Close(ctx, tk.Ref)

	for _, want := range []EventKind{EventCreated, EventClosed} {
		select {
		case ev := <-ch:
			if ev.Kind != want || ev.Ref.ID != "ws1-a" {
				t.Fatalf("event = %+v, want %s", ev, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// TestBeadsSubprocess runs the adapter against a fake bd executable in a temp
// repo, exercising the real exec path: command construction, working directory,
// and stdout parsing.
func TestBeadsSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd is a shell script")
	}
	dir := t.TempDir()
	// A fake bd: answers create/show with canned JSON and records its cwd.
	script := "#!/bin/sh\n" +
		"echo \"$PWD\" > \"$PWD/bd-cwd\"\n" +
		"case \"$1\" in\n" +
		"create) printf '{\"id\":\"ws1-x\",\"title\":\"t\",\"status\":\"open\"}' ;;\n" +
		"show) printf '[{\"id\":\"ws1-x\",\"title\":\"t\",\"status\":\"open\",\"description\":\"d\"}]' ;;\n" +
		"*) echo unknown >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "fakebd")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	b := NewBeads("beads", "ws1", dir)
	b.bin = bin

	tk, err := b.Create(context.Background(), CreateParams{Title: "t"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tk.Ref.ID != "ws1-x" {
		t.Errorf("created ref = %+v", tk.Ref)
	}
	got, err := b.Show(context.Background(), tk.Ref)
	if err != nil || got.Description != "d" {
		t.Fatalf("Show = %+v, err = %v", got, err)
	}
	// The subprocess ran in the provider's directory.
	cwd, err := os.ReadFile(filepath.Join(dir, "bd-cwd"))
	if err != nil {
		t.Fatalf("reading recorded cwd: %v", err)
	}
	if got := filepath.Clean(string(cwd[:len(cwd)-1])); got != filepath.Clean(dir) {
		t.Errorf("bd ran in %q, want %q", got, dir)
	}
}
