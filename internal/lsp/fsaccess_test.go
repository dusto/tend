package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/worktree"
)

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

func newGatedSvc(t *testing.T, ed editorClient, ap approver, opts Options) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	return NewService(r, ed, ap, opts), root
}

func TestDiagnosticsOutsideWorktreeApprovedQueries(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	ed := &fakeEditor{open: false}
	svc, _ := newGatedSvc(t, ed, ap, Options{})

	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: fileURI(outside)}); err != nil {
		t.Fatalf("approved outside diagnostics: %v", err)
	}
	if ap.kind != api.ApprovalFilesystemAccess {
		t.Errorf("approval kind = %q, want filesystem_access", ap.kind)
	}
	var det api.ApprovalDetail
	if err := json.Unmarshal(ap.detail, &det); err != nil {
		t.Fatal(err)
	}
	if det.FilesystemAccess == nil || det.FilesystemAccess.Mode != api.FilesystemModeDiagnostics {
		t.Errorf("detail = %+v, want diagnostics mode", det)
	}
}

func TestDiagnosticsOutsideWorktreeDeniedRefuses(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false}}
	ed := &fakeEditor{open: false}
	svc, _ := newGatedSvc(t, ed, ap, Options{})

	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: fileURI(outside)}); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("denied outside diagnostics err = %v, want ErrAccessDenied", err)
	}
}

func TestDiagnosticsOutsideWorktreeNoApproverHardDenies(t *testing.T) {
	ed := &fakeEditor{open: false}
	svc, _ := newGatedSvc(t, ed, nil, Options{})
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: fileURI(outside)}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("no-approver outside diagnostics err = %v, want ErrOutsideWorkspace", err)
	}
}

func TestDiagnosticsExtraReadableRootNoPrompt(t *testing.T) {
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	extra := t.TempDir()
	f := filepath.Join(extra, "dep.go")
	if err := os.WriteFile(f, []byte("package dep"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := &fakeEditor{open: true}
	svc, _ := newGatedSvc(t, ed, ap, Options{ExtraReadableRoots: []string{extra}})

	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: fileURI(f)}); err != nil {
		t.Fatalf("extra-root diagnostics: %v", err)
	}
	if ap.kind != "" {
		t.Errorf("approver called (kind=%q); an extra root must not prompt", ap.kind)
	}
}
