package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

// uriEditor is a fake editor that reports specific URIs as open buffers and
// treats every other URI as closed (ErrEditorUnavailable -> disk).
type uriEditor struct {
	open      map[string]api.EditorReadBufferResult
	writeBase api.FileBase
	wrote     []api.EditorWriteBufferParams
}

func (e *uriEditor) ReadBuffer(_ context.Context, _ api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error) {
	if r, ok := e.open[p.URI]; ok {
		return r, nil
	}
	return api.EditorReadBufferResult{}, editor.ErrEditorUnavailable
}

func (e *uriEditor) WriteBuffer(_ context.Context, _ api.SessionID, p api.EditorWriteBufferParams) (api.EditorWriteBufferResult, error) {
	e.wrote = append(e.wrote, p)
	return api.EditorWriteBufferResult{Base: e.writeBase}, nil
}

func (e *uriEditor) Open(_ context.Context, _ api.SessionID, _ api.EditorOpenParams) (api.EditorOpenResult, error) {
	return api.EditorOpenResult{}, nil
}

// newChangeSet builds a change-set service over a temp worktree with the given
// editor and approver, and returns the service and the worktree root.
func newChangeSet(t *testing.T, ed editorClient, ap approver) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	svc := NewService(r, ed, ap, Options{NewChangeSetID: func() api.ChangeSetID { return "cs1" }})
	return svc, root
}

func seed(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return fileURI(path)
}

func writeChange(uri, content string, base api.FileBase) api.FileChange {
	return api.FileChange{URI: uri, Base: base, Kind: api.FileChangeWrite, Content: content}
}

func TestApplyChangeSetAllDiskApplied(t *testing.T) {
	ed := &uriEditor{}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root := newChangeSet(t, ed, ap)
	a := seed(t, root, "a.txt", "aaa\n")
	b := seed(t, root, "b.txt", "bbb\n")

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			writeChange(a, "AAA\n", diskBase("aaa\n")),
			writeChange(b, "BBB\n", diskBase("bbb\n")),
		},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if !res.Applied || res.ChangeSetID != "cs1" || len(res.Files) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for i, f := range res.Files {
		if !f.Applied || f.Base.ContentHash == "" {
			t.Errorf("file %d = %+v", i, f)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "AAA\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "BBB\n" {
		t.Errorf("b.txt = %q", got)
	}
	// One approval carried both targets.
	var detail api.ApprovalDetail
	_ = json.Unmarshal(ap.detail, &detail)
	if detail.FileEdit == nil || len(detail.FileEdit.Targets) != 2 {
		t.Errorf("approval targets = %+v", detail.FileEdit)
	}
}

func TestApplyChangeSetPreflightConflictAborts(t *testing.T) {
	ed := &uriEditor{}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root := newChangeSet(t, ed, ap)
	a := seed(t, root, "a.txt", "aaa\n")
	b := seed(t, root, "b.txt", "bbb\n")

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			writeChange(a, "AAA\n", diskBase("aaa\n")),
			writeChange(b, "BBB\n", diskBase("STALE\n")), // base does not match disk
		},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if res.Applied || res.Files[1].Error == "" {
		t.Fatalf("result = %+v, want aborted with conflict on b", res)
	}
	if ap.kind != "" {
		t.Error("preflight conflict must abort before requesting approval")
	}
	// Nothing applied: both files unchanged.
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "aaa\n" {
		t.Errorf("a.txt changed despite abort: %q", got)
	}
}

func TestApplyChangeSetDenied(t *testing.T) {
	ed := &uriEditor{}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false, Reason: "no"}}
	svc, root := newChangeSet(t, ed, ap)
	a := seed(t, root, "a.txt", "aaa\n")

	res, _ := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes:   []api.FileChange{writeChange(a, "AAA\n", diskBase("aaa\n"))},
	})
	if res.Applied || res.Reason != "no" {
		t.Fatalf("result = %+v, want denied", res)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "aaa\n" {
		t.Errorf("a.txt changed despite denial: %q", got)
	}
}

func TestApplyChangeSetMixedOpenAndClosed(t *testing.T) {
	tick, newTick := int64(4), int64(5)
	ed := &uriEditor{writeBase: api.FileBase{ChangedTick: &newTick}}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root := newChangeSet(t, ed, ap)
	// The open file is still under the worktree (containment) but served live by
	// the editor; its disk content is ignored in favor of the buffer.
	openURI := seed(t, root, "open.txt", "ignored-on-disk\n")
	ed.open = map[string]api.EditorReadBufferResult{openURI: {Content: "old\n", Base: api.FileBase{ChangedTick: &tick}, Open: true}}
	disk := seed(t, root, "disk.txt", "disk\n")

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			writeChange(disk, "DISK\n", diskBase("disk\n")),
			writeChange(openURI, "NEW\n", api.FileBase{ChangedTick: &tick}),
		},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if !res.Applied {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "disk.txt")); string(got) != "DISK\n" {
		t.Errorf("disk.txt = %q", got)
	}
	if len(ed.wrote) != 1 || ed.wrote[0].Content != "NEW\n" {
		t.Errorf("editor writes = %+v", ed.wrote)
	}
}

func TestApplyChangeSetRollsBackDiskOnMidApplyConflict(t *testing.T) {
	ed := &uriEditor{}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root := newChangeSet(t, ed, ap)
	a := seed(t, root, "a.txt", "aaa\n")
	b := seed(t, root, "b.txt", "bbb\n")

	// While the approval is pending, b.txt changes on disk, so its apply-time base
	// re-check conflicts after a.txt has already been written.
	ap.onRequest = func() {
		_ = os.WriteFile(filepath.Join(root, "b.txt"), []byte("CHANGED\n"), 0o644)
	}

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			writeChange(a, "AAA\n", diskBase("aaa\n")),
			writeChange(b, "BBB\n", diskBase("bbb\n")),
		},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if res.Applied {
		t.Fatalf("result should not be fully applied: %+v", res)
	}
	if !res.Files[0].RolledBack || res.Files[0].Applied {
		t.Errorf("a should be rolled back: %+v", res.Files[0])
	}
	if res.Files[1].Error == "" {
		t.Errorf("b should report a conflict: %+v", res.Files[1])
	}
	// a.txt restored to its original content; b.txt left as the concurrent change.
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "aaa\n" {
		t.Errorf("a.txt not rolled back: %q", got)
	}
}

func TestApplyChangeSetUnknownSession(t *testing.T) {
	svc, _ := newChangeSet(t, &uriEditor{}, &fakeApprover{outcome: approvals.Outcome{Approved: true}})
	if _, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{SessionID: "nope"}); err == nil {
		t.Error("unknown session should error")
	}
}

func TestApplyChangeSetInvalidPatchAborts(t *testing.T) {
	ed := &uriEditor{}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root := newChangeSet(t, ed, ap)
	a := seed(t, root, "a.txt", "aaa\n")

	res, _ := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{{
			URI: a, Base: diskBase("aaa\n"), Kind: api.FileChangePatch,
			Edits: []api.TextEdit{{Range: api.Range{Start: api.Position{Line: 9}, End: api.Position{Line: 9}}, NewText: "x"}},
		}},
	})
	if res.Applied || res.Files[0].Error == "" {
		t.Fatalf("result = %+v, want aborted with an edit error", res)
	}
	if ap.kind != "" {
		t.Error("an invalid patch must abort before approval")
	}
}
