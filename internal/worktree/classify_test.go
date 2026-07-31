package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyPathInside(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "pkg", "main.go")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, inside, err := ClassifyPath("file://"+f, root)
	if err != nil {
		t.Fatalf("ClassifyPath: %v", err)
	}
	if !inside {
		t.Errorf("inside = false, want true for a path within the worktree")
	}
	if resolved == "" {
		t.Errorf("resolved path is empty")
	}
}

func TestClassifyPathOutsideIsNotAnError(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, inside, err := ClassifyPath("file://"+outside, root)
	if err != nil {
		t.Fatalf("outside path should classify without error, got %v", err)
	}
	if inside {
		t.Errorf("inside = true, want false for a path outside the worktree")
	}
	if resolved == "" {
		t.Errorf("resolved should still be returned for an outside path")
	}
}

func TestClassifyPathSymlinkEscapeIsOutside(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the worktree pointing outside classifies as outside (the
	// resolved target escapes), so a link cannot disguise an outside read.
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	resolved, inside, err := ClassifyPath("file://"+link, root)
	if err != nil {
		t.Fatalf("ClassifyPath: %v", err)
	}
	if inside {
		t.Errorf("inside = true, want false: a symlink escaping the worktree is outside")
	}
	if resolved != secret {
		t.Errorf("resolved = %q, want the symlink target %q", resolved, secret)
	}
}

func TestClassifyPathBadURI(t *testing.T) {
	if _, _, err := ClassifyPath("not-a-file-uri", t.TempDir()); err == nil {
		t.Error("a non-file uri should return ErrBadURI, got nil")
	}
}

func TestClassifyPathExtraRootInside(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	f := filepath.Join(extra, "mod", "go.mod")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A path under a configured extra readable root classifies as inside, so it
	// is read without prompting.
	_, inside, err := ClassifyPath("file://"+f, root, extra)
	if err != nil {
		t.Fatalf("ClassifyPath: %v", err)
	}
	if !inside {
		t.Errorf("inside = false, want true for a path under an extra readable root")
	}
}
