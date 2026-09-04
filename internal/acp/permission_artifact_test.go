package acp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/session"
)

// artifactEmitter records published artifact_written events.
type artifactEmitter struct{ arts []api.ArtifactWritten }

func (e *artifactEmitter) Publish(ev api.Event) (api.Event, error) {
	if ev.Type == "artifact_written" {
		var a api.ArtifactWritten
		if json.Unmarshal(ev.Payload, &a) == nil {
			e.arts = append(e.arts, a)
		}
	}
	return ev, nil
}

// approveRouter builds a router that approves everything, resolves one session
// rooted at worktree, and records artifacts through em.
func approveRouter(worktree string, em acp.Emitter) *acp.PermissionRouter {
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1", WorkspaceID: "ws1", WorktreeRoot: worktree}, ok: true}
	return acp.NewPermissionRouter(&recordingNext{}, gate, lookup, nil, em)
}

// callWrite drives one approved request_permission carrying rawInput and returns
// the emitted artifacts.
func callWrite(t *testing.T, worktree string, rawInput map[string]any) []api.ArtifactWritten {
	t.Helper()
	em := &artifactEmitter{}
	r := approveRouter(worktree, em)
	params := map[string]any{
		"sessionId": "s1",
		"toolCall": map[string]any{
			"toolCallId": "tc-1", "title": "Write", "kind": "edit", "rawInput": rawInput,
		},
		"options": standardOptions(),
	}
	if _, err := r.Handle(context.Background(), permReq(t, params)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return em.arts
}

func TestArtifactFromNativeWrite(t *testing.T) {
	root := t.TempDir()
	// A brand-new file: old content is empty, diff is all additions.
	arts := callWrite(t, root, map[string]any{
		"file_path": filepath.Join(root, "notes.md"),
		"content":   "# Title\n",
	})
	if len(arts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(arts))
	}
	a := arts[0]
	if a.Content != "# Title\n" {
		t.Errorf("content = %q", a.Content)
	}
	if !strings.HasSuffix(a.URI, "/notes.md") {
		t.Errorf("uri = %q", a.URI)
	}
	if a.Diff == "" {
		t.Errorf("expected a diff for the new file")
	}
}

func TestArtifactFromNativeEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	arts := callWrite(t, root, map[string]any{
		"file_path":  path,
		"old_string": "old",
		"new_string": "new",
	})
	if len(arts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(arts))
	}
	if arts[0].Content != "package new\n" {
		t.Errorf("edit result = %q, want the replacement applied", arts[0].Content)
	}
	if !strings.Contains(arts[0].Diff, "old") || !strings.Contains(arts[0].Diff, "new") {
		t.Errorf("diff should show old→new: %q", arts[0].Diff)
	}
}

func TestArtifactFromNativeMultiEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("a b c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	arts := callWrite(t, root, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "A"},
			{"old_string": "c", "new_string": "C"},
		},
	})
	if len(arts) != 1 || arts[0].Content != "A b C\n" {
		t.Fatalf("multiedit result = %+v, want 'A b C'", arts)
	}
}

func TestNoArtifactForNonWriteTool(t *testing.T) {
	// A Bash command carries no file write shape → no artifact.
	arts := callWrite(t, t.TempDir(), map[string]any{"command": "ls -la"})
	if len(arts) != 0 {
		t.Errorf("non-write tool should emit no artifact, got %+v", arts)
	}
}

func TestNoArtifactOutsideWorktree(t *testing.T) {
	root := t.TempDir()
	arts := callWrite(t, root, map[string]any{
		"file_path": "/etc/passwd", "content": "x",
	})
	if len(arts) != 0 {
		t.Errorf("a write outside the worktree must not emit (or read) an artifact, got %+v", arts)
	}
}

func TestNoArtifactWhenDenied(t *testing.T) {
	root := t.TempDir()
	em := &artifactEmitter{}
	gate := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	lookup := fakeLookup{sess: &session.Session{ID: "s1", WorktreeRoot: root}, ok: true}
	r := acp.NewPermissionRouter(&recordingNext{}, gate, lookup, nil, em)
	params := map[string]any{
		"sessionId": "s1",
		"toolCall":  map[string]any{"toolCallId": "tc-1", "rawInput": map[string]any{"file_path": filepath.Join(root, "f"), "content": "x"}},
		"options":   standardOptions(),
	}
	if _, err := r.Handle(context.Background(), permReq(t, params)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(em.arts) != 0 {
		t.Errorf("a denied write must not emit an artifact, got %+v", em.arts)
	}
}
