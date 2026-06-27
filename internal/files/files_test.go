package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

// fakeEditor stands in for editor.Service: it returns a canned read result or an
// error (e.g. editor.ErrEditorUnavailable for a headless session), and records
// writes routed to it.
type fakeEditor struct {
	res  api.EditorReadBufferResult
	err  error
	uris []string

	wrote     *api.EditorWriteBufferParams // last write routed here
	writeBase api.FileBase                 // base returned from a write
}

func (f *fakeEditor) ReadBuffer(_ context.Context, _ api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error) {
	f.uris = append(f.uris, p.URI)
	return f.res, f.err
}

func (f *fakeEditor) WriteBuffer(_ context.Context, _ api.SessionID, p api.EditorWriteBufferParams) (api.EditorWriteBufferResult, error) {
	cp := p
	f.wrote = &cp
	return api.EditorWriteBufferResult{Base: f.writeBase}, nil
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// newService creates a session rooted at a temp worktree and a file service with
// the given fake editor, returning the service and the worktree root. The
// approver is nil (the read tests do not mutate); mutation tests use newMutator.
func newService(t *testing.T, ed editorClient) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	r := session.NewRegistry()
	r.Create("s1", "codex", "ws1", api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"}, root)
	return NewService(r, ed, nil, Options{}), root
}

func TestReadFromDiskWhenHeadless(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, root := newService(t, ed)

	path := filepath.Join(root, "a.go")
	content := "package a\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(path)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Open {
		t.Error("Open should be false for a disk read")
	}
	if res.Content != content {
		t.Errorf("content = %q", res.Content)
	}
	sum := sha256.Sum256([]byte(content))
	if res.Base.ContentHash != hex.EncodeToString(sum[:]) {
		t.Errorf("content hash = %q", res.Base.ContentHash)
	}
	if res.Base.ChangedTick != nil {
		t.Error("disk read should not carry a changedtick")
	}
}

func TestReadFromOpenBuffer(t *testing.T) {
	tick := int64(12)
	ed := &fakeEditor{res: api.EditorReadBufferResult{Content: "live\n", Base: api.FileBase{ChangedTick: &tick}, Open: true}}
	svc, root := newService(t, ed)

	// A stale copy on disk must be ignored in favor of the live buffer.
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(path)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !res.Open || res.Content != "live\n" || res.Base.ChangedTick == nil || *res.Base.ChangedTick != 12 {
		t.Fatalf("result = %+v", res)
	}
}

func TestOpenFalseFallsBackToDisk(t *testing.T) {
	// The editor is bound but the file is not open there: read disk for a
	// canonical content hash rather than trusting the editor's disk read.
	ed := &fakeEditor{res: api.EditorReadBufferResult{Content: "ignored", Open: false}}
	svc, root := newService(t, ed)
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("disk\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(path)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Open || res.Content != "disk\n" || res.Base.ContentHash == "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestReadSurfacesEditorError(t *testing.T) {
	ed := &fakeEditor{err: errors.New("reverse call failed")}
	svc, root := newService(t, ed)
	path := filepath.Join(root, "a.go")
	_ = os.WriteFile(path, []byte("x"), 0o644)

	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(path)}); err == nil {
		t.Error("a genuine reverse-call failure should be surfaced, not masked by a disk read")
	}
}

func TestReadUnknownSession(t *testing.T) {
	svc, root := newService(t, &fakeEditor{err: editor.ErrEditorUnavailable})
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "nope", URI: fileURI(filepath.Join(root, "a.go"))}); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestReadRejectsPathOutsideWorktree(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, _ := newService(t, ed)
	// A path-traversal uri that escapes the worktree must be refused.
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: "file:///etc/passwd"}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("err = %v, want ErrOutsideWorkspace", err)
	}
}

func TestReadRejectsSymlinkEscape(t *testing.T) {
	ed := &fakeEditor{err: editor.ErrEditorUnavailable}
	svc, root := newService(t, ed)

	// A secret outside the worktree, reachable through a symlink inside it.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// A symlinked directory inside the worktree pointing outside it.
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(filepath.Join(root, "link", "secret.txt"))}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("through symlinked dir: err = %v, want ErrOutsideWorkspace", err)
	}

	// A symlinked file inside the worktree pointing at the secret directly.
	if err := os.Symlink(secret, filepath.Join(root, "ln.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: fileURI(filepath.Join(root, "ln.txt"))}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("through symlinked file: err = %v, want ErrOutsideWorkspace", err)
	}
}

func TestReadRejectsNonFileURI(t *testing.T) {
	svc, _ := newService(t, &fakeEditor{err: editor.ErrEditorUnavailable})
	if _, err := svc.Read(context.Background(), api.FileReadParams{SessionID: "s1", URI: "http://example.com/a"}); !errors.Is(err, ErrBadURI) {
		t.Errorf("err = %v, want ErrBadURI", err)
	}
}
