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

func TestDiagnosticsOutsideWorktreeNoPromptCapableHardDenies(t *testing.T) {
	// Gate wired, but no client can answer: hard-deny rather than block, and do
	// not raise the prompt.
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	ed := &fakeEditor{open: false}
	svc, _ := newGatedSvc(t, ed, ap, Options{PromptCapable: func() bool { return false }})

	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: fileURI(outside)}); !errors.Is(err, worktree.ErrOutsideWorkspace) {
		t.Errorf("headless outside diagnostics err = %v, want ErrOutsideWorkspace", err)
	}
	if ap.kind != "" {
		t.Errorf("approver called (kind=%q); headless outside read must not prompt", ap.kind)
	}
}

func TestFilesystemAccessToolReflectsCallingMethod(t *testing.T) {
	// Each LSP read must label the filesystem_access approval with its OWN method,
	// not a hardcoded lsp.diagnostics — the user consents to a concrete operation.
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := fileURI(outside)

	cases := []struct {
		tool string
		call func(svc *Service) error
	}{
		{MethodDiagnostics, func(svc *Service) error {
			_, err := svc.Diagnostics(context.Background(), api.LSPDiagnosticsParams{SessionID: "s1", URI: uri})
			return err
		}},
		{MethodSymbols, func(svc *Service) error {
			_, err := svc.Symbols(context.Background(), api.LSPSymbolsParams{SessionID: "s1", URI: uri})
			return err
		}},
		{MethodDefinition, func(svc *Service) error {
			_, err := svc.Definition(context.Background(), api.LSPDefinitionParams{SessionID: "s1", URI: uri})
			return err
		}},
		{MethodReferences, func(svc *Service) error {
			_, err := svc.References(context.Background(), api.LSPReferencesParams{SessionID: "s1", URI: uri})
			return err
		}},
		{MethodHover, func(svc *Service) error {
			_, err := svc.Hover(context.Background(), api.LSPHoverParams{SessionID: "s1", URI: uri})
			return err
		}},
		{MethodCodeActions, func(svc *Service) error {
			_, err := svc.CodeActions(context.Background(), api.LSPCodeActionsParams{SessionID: "s1", URI: uri})
			return err
		}},
	}
	for _, tc := range cases {
		ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
		ed := &fakeEditor{open: false}
		svc, _ := newGatedSvc(t, ed, ap, Options{})
		if err := tc.call(svc); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		var det api.ApprovalDetail
		if err := json.Unmarshal(ap.detail, &det); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.tool, err)
		}
		if det.FilesystemAccess == nil || det.FilesystemAccess.Tool != tc.tool {
			t.Errorf("tool = %q, want %q (the calling method)", det.FilesystemAccess.Tool, tc.tool)
		}
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
