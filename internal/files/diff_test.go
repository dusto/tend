package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

func TestPatchRecordsSnapshotsForDiff(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "old\n")

	if _, err := svc.Write(context.Background(), api.FileWriteParams{
		SessionID: "s1", URI: fileURI(path), Content: "new\n", Base: diskBase("old\n"),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "cs1"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if res.ChangeSetID != "cs1" || res.SessionID != "s1" || !res.Applied {
		t.Fatalf("result = %+v, want applied cs1 for s1", res)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files = %+v, want one entry", res.Files)
	}
	f := res.Files[0]
	if f.URI != fileURI(path) || f.Before != "old\n" || f.After != "new\n" || !f.Applied {
		t.Errorf("entry = %+v", f)
	}
	if !strings.Contains(f.Diff, "-old") || !strings.Contains(f.Diff, "+new") {
		t.Errorf("diff missing the change:\n%s", f.Diff)
	}
}

func TestDeniedProposalStillDiffable(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: false, Reason: "nope"}}
	svc, _, path := newMutator(t, ed, ap, "old\n")

	if _, err := svc.Write(context.Background(), api.FileWriteParams{
		SessionID: "s1", URI: fileURI(path), Content: "new\n", Base: diskBase("old\n"),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "cs1"})
	if err != nil {
		t.Fatalf("a denied proposal must stay reviewable: %v", err)
	}
	if res.Applied || res.Files[0].Applied {
		t.Errorf("result = %+v, want not applied", res)
	}
	if res.Files[0].Before != "old\n" || res.Files[0].After != "new\n" {
		t.Errorf("snapshots = %+v, want the proposal as proposed", res.Files[0])
	}
}

func TestDiffUnknownChangeSet(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _, _ := newMutator(t, ed, &fakeApprover{}, "x\n")

	if _, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "zz"}); !errors.Is(err, ErrUnknownChangeSet) {
		t.Errorf("err = %v, want ErrUnknownChangeSet", err)
	}
}

func TestSnapshotRetentionEvictsOldest(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	n := 0
	svc := NewService(r, ed, ap, Options{
		NewChangeSetID:   func() api.ChangeSetID { n++; return api.ChangeSetID(fmt.Sprintf("cs%d", n)) },
		RetainChangeSets: 2,
	})
	path := filepath.Join(root, "a.txt")
	content := "v0\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for i := 1; i <= 3; i++ {
		next := fmt.Sprintf("v%d\n", i)
		if _, err := svc.Write(context.Background(), api.FileWriteParams{
			SessionID: "s1", URI: fileURI(path), Content: next, Base: diskBase(content),
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		content = next
	}

	if _, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "cs1"}); !errors.Is(err, ErrUnknownChangeSet) {
		t.Errorf("cs1 err = %v, want evicted beyond the retention cap", err)
	}
	for _, id := range []api.ChangeSetID{"cs2", "cs3"} {
		if _, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: id}); err != nil {
			t.Errorf("%s: %v, want retained", id, err)
		}
	}
}

func TestApplyChangeSetRecordsSnapshots(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, root, path := newMutator(t, ed, ap, "one\n")
	path2 := filepath.Join(root, "b.go")
	if err := os.WriteFile(path2, []byte("two\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			{URI: fileURI(path), Base: diskBase("one\n"), Kind: api.FileChangeWrite, Content: "ONE\n"},
			{URI: fileURI(path2), Base: diskBase("two\n"), Kind: api.FileChangeWrite, Content: "TWO\n"},
		},
	})
	if err != nil || !res.Applied {
		t.Fatalf("ApplyChangeSet = %+v, err = %v", res, err)
	}

	d, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "cs1"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !d.Applied || len(d.Files) != 2 {
		t.Fatalf("diff = %+v, want two applied entries", d)
	}
	want := map[string][2]string{
		fileURI(path):  {"one\n", "ONE\n"},
		fileURI(path2): {"two\n", "TWO\n"},
	}
	for _, f := range d.Files {
		w, ok := want[f.URI]
		if !ok {
			t.Errorf("unexpected entry %q", f.URI)
			continue
		}
		if f.Before != w[0] || f.After != w[1] || !f.Applied || f.Diff == "" {
			t.Errorf("entry = %+v, want %v applied with a diff", f, w)
		}
	}
}

func TestPreflightAbortedSetNotRecorded(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	ap := &fakeApprover{outcome: approvals.Outcome{Approved: true}}
	svc, _, path := newMutator(t, ed, ap, "one\n")

	res, err := svc.ApplyChangeSet(context.Background(), api.FileApplyChangeSetParams{
		SessionID: "s1",
		Changes: []api.FileChange{
			{URI: fileURI(path), Base: diskBase("STALE\n"), Kind: api.FileChangeWrite, Content: "ONE\n"},
		},
	})
	if err != nil || res.Applied {
		t.Fatalf("ApplyChangeSet = %+v, err = %v, want aborted", res, err)
	}
	// Nothing was proposed past preflight, so there is nothing to review.
	if _, err := svc.Diff(context.Background(), api.FileDiffParams{ChangeSetID: "cs1"}); !errors.Is(err, ErrUnknownChangeSet) {
		t.Errorf("err = %v, want ErrUnknownChangeSet for an aborted set", err)
	}
}
