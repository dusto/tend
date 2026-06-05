package files

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/patch"
	"github.com/dusto/tend/internal/session"
)

// fakeApprover records the requested approval and returns a canned outcome.
type fakeApprover struct {
	outcome approvals.Outcome
	err     error
	kind    string
	detail  json.RawMessage
}

func (a *fakeApprover) Request(_ context.Context, _ *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error) {
	a.kind = kind
	a.detail = detail
	return a.outcome, a.err
}

// newMutator builds a file service over a temp worktree with the given editor and
// approver, a fixed change-set id, and returns the service, worktree root, and a
// path to a seeded file containing seed.
func newMutator(t *testing.T, ed editorClient, ap approver, seed string) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	svc := NewService(r, ed, ap, Options{NewChangeSetID: func() api.ChangeSetID { return "cs1" }})
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return svc, root, path
}

func diskBase(content string) api.FileBase {
	return api.FileBase{ContentHash: patch.ContentHash([]byte(content))}
}

func TestPatchAppliedToDisk(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "package a\n")

	res, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 0, ByteCol: 9}, End: api.Position{Line: 0, ByteCol: 9}}, NewText: "b"}},
		Base:  diskBase("package a\n"),
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !res.Applied || res.ChangeSetID != "cs1" {
		t.Fatalf("result = %+v", res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package ab\n" {
		t.Errorf("file = %q, want %q", got, "package ab\n")
	}
	if res.Base.ContentHash != patch.ContentHash(got) {
		t.Errorf("result base hash = %q, want hash of new content", res.Base.ContentHash)
	}
	if ap.kind != api.ApprovalFileEdit {
		t.Errorf("approval kind = %q, want %q", ap.kind, api.ApprovalFileEdit)
	}
}

func TestPatchApprovalDetailIsSelfContained(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "a\nb\nc\n")

	if _, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 1, ByteCol: 0}, End: api.Position{Line: 1, ByteCol: 1}}, NewText: "B"}},
		Base:  diskBase("a\nb\nc\n"),
	}); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	var detail api.ApprovalDetail
	if err := json.Unmarshal(ap.detail, &detail); err != nil {
		t.Fatalf("approval detail: %v", err)
	}
	if detail.Kind != api.ApprovalFileEdit || detail.FileEdit == nil {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.FileEdit.ChangeSetID != "cs1" || len(detail.FileEdit.Targets) != 1 {
		t.Fatalf("file edit = %+v", detail.FileEdit)
	}
	tgt := detail.FileEdit.Targets[0]
	if tgt.URI != fileURI(path) || tgt.Base.ContentHash == "" {
		t.Errorf("target = %+v", tgt)
	}
	// The diff shows the b -> B line change, so a non-editor client can review it.
	if !strings.Contains(tgt.Diff, "-b") || !strings.Contains(tgt.Diff, "+B") {
		t.Errorf("diff missing the change:\n%s", tgt.Diff)
	}
}

func TestWriteAppliedToDisk(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "old\n")

	res, err := svc.Write(context.Background(), api.FileWriteParams{
		SessionID: "s1", URI: fileURI(path), Content: "new contents\n", Base: diskBase("old\n"),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !res.Applied {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(path); string(got) != "new contents\n" {
		t.Errorf("file = %q", got)
	}
}

func TestPatchDeniedNotApplied(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false, Reason: "nope"}}
	svc, _, path := newMutator(t, ed, ap, "package a\n")

	res, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 0, ByteCol: 0}, End: api.Position{Line: 0, ByteCol: 7}}, NewText: "X"}},
		Base:  diskBase("package a\n"),
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if res.Applied || res.Reason != "nope" {
		t.Errorf("result = %+v, want not-applied with reason", res)
	}
	if got, _ := os.ReadFile(path); string(got) != "package a\n" {
		t.Errorf("file changed despite denial: %q", got)
	}
}

func TestPatchStaleBaseConflicts(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "current\n")

	// Cite a base for content that no longer matches what is on disk.
	_, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 0, ByteCol: 0}, End: api.Position{Line: 0, ByteCol: 0}}, NewText: "x"}},
		Base:  diskBase("STALE\n"),
	})
	if !errors.Is(err, patch.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if ap.kind != "" {
		t.Error("a stale base must be caught before requesting approval")
	}
	if got, _ := os.ReadFile(path); string(got) != "current\n" {
		t.Errorf("file changed on conflict: %q", got)
	}
}

func TestPatchInvalidEditRejected(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "ab\n")

	_, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 9, ByteCol: 0}, End: api.Position{Line: 9, ByteCol: 0}}, NewText: "x"}},
		Base:  diskBase("ab\n"),
	})
	if !errors.Is(err, patch.ErrInvalidPosition) {
		t.Errorf("err = %v, want ErrInvalidPosition", err)
	}
}

func TestPatchOpenBufferRoutesToEditor(t *testing.T) {
	tick, newTick := int64(5), int64(6)
	ed := &fakeEditor{
		res:       api.EditorReadBufferResult{Content: "live\n", Base: api.FileBase{ChangedTick: &tick}, Open: true},
		writeBase: api.FileBase{ChangedTick: &newTick},
	}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "stale on disk\n")

	res, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 0, ByteCol: 4}, End: api.Position{Line: 0, ByteCol: 4}}, NewText: "!"}},
		Base:  api.FileBase{ChangedTick: &tick},
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !res.Applied || res.Base.ChangedTick == nil || *res.Base.ChangedTick != 6 {
		t.Fatalf("result = %+v, want applied with new changedtick", res)
	}
	// The edit went to the editor, not disk.
	if ed.wrote == nil || ed.wrote.Content != "live!\n" {
		t.Fatalf("editor write = %+v", ed.wrote)
	}
	if got, _ := os.ReadFile(path); string(got) != "stale on disk\n" {
		t.Errorf("disk should be untouched for an open buffer: %q", got)
	}
}

func TestPatchOpenBufferStaleChangedtickConflicts(t *testing.T) {
	cur := int64(9)
	ed := &fakeEditor{res: api.EditorReadBufferResult{Content: "x\n", Base: api.FileBase{ChangedTick: &cur}, Open: true}}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "x\n")

	stale := int64(8)
	_, err := svc.Patch(context.Background(), api.FilePatchParams{
		SessionID: "s1", URI: fileURI(path),
		Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 0, ByteCol: 0}, End: api.Position{Line: 0, ByteCol: 0}}, NewText: "y"}},
		Base:  api.FileBase{ChangedTick: &stale},
	})
	if !errors.Is(err, patch.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict for a stale changedtick", err)
	}
}

func TestMutateUnknownSession(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _, path := newMutator(t, ed, &fakeApprover{outcome: approvals.Outcome{Approved: true}}, "x\n")
	if _, err := svc.Write(context.Background(), api.FileWriteParams{SessionID: "nope", URI: fileURI(path), Base: diskBase("x\n")}); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}
