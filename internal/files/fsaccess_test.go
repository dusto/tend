package files

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

// newGatedReadService is like newService but wires an approver, so outside reads
// are gated rather than hard-denied.
func newGatedReadService(t *testing.T, ed editorClient, ap approver, opts Options) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	return NewService(r, ed, ap, opts), root
}

func TestReadOutsideWorktreeApprovedReads(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _ := newGatedReadService(t, ed, ap, Options{})

	// A secret outside the worktree, named directly.
	outside := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(outside, []byte("outside content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(outside)})
	if err != nil {
		t.Fatalf("approved outside read: %v", err)
	}
	if res.Content != "outside content\n" {
		t.Errorf("content = %q, want the outside file's content", res.Content)
	}
	// It was gated as filesystem_access, read mode, carrying the resolved path.
	if ap.kind != api.ApprovalFilesystemAccess {
		t.Errorf("approval kind = %q, want filesystem_access", ap.kind)
	}
	var det api.ApprovalDetail
	if err := json.Unmarshal(ap.detail, &det); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if det.FilesystemAccess == nil || det.FilesystemAccess.Mode != api.FilesystemModeRead {
		t.Fatalf("detail = %+v, want filesystem_access read", det)
	}
	if det.FilesystemAccess.ResolvedPath != outside {
		t.Errorf("resolved path = %q, want %q", det.FilesystemAccess.ResolvedPath, outside)
	}
}

func TestReadOutsideWorktreeDeniedRefuses(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false, Reason: "no"}}
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _ := newGatedReadService(t, ed, ap, Options{})

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(outside)}); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("denied outside read err = %v, want ErrAccessDenied", err)
	}
}

func TestReadOutsideWorktreeTOCTOURefuses(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}

	dir := t.TempDir()
	realA := filepath.Join(dir, "a.txt")
	realB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(realA, []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realB, []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, root := newGatedReadService(t, ed, nil, Options{})
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(realA, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The approval is granted, but between prompt and read the symlink is
	// repointed from a.txt to b.txt — the re-resolved target differs from the
	// approved one, so the read is refused.
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}, onRequest: func() {
		_ = os.Remove(link)
		_ = os.Symlink(realB, link)
	}}
	svc.approver = ap

	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(link)}); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("TOCTOU repoint err = %v, want ErrAccessDenied", err)
	}
}

func TestReadExtraReadableRootNoPrompt(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}

	extra := t.TempDir()
	f := filepath.Join(extra, "go.mod")
	if err := os.WriteFile(f, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, _ := newGatedReadService(t, ed, ap, Options{ExtraReadableRoots: []string{extra}})

	res, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(f)})
	if err != nil {
		t.Fatalf("extra-root read: %v", err)
	}
	if res.Content != "module x\n" {
		t.Errorf("content = %q", res.Content)
	}
	// A configured extra readable root resolves in-scope WITHOUT prompting.
	if ap.kind != "" {
		t.Errorf("approver was called (kind=%q); an extra root must not prompt", ap.kind)
	}
}

func TestReadOutsideWorktreeNoApproverHardDenies(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _ := newGatedReadService(t, ed, nil, Options{})
	outside := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With no approver wired, an outside read cannot be gated, so it stays
	// hard-denied rather than silently allowed.
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(outside)}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("no-approver outside read err = %v, want ErrOutsideWorkspace", err)
	}
}
