package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a git repo with one commit in a fresh temp dir and returns
// its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func identify(t *testing.T, dir string) Identity {
	t.Helper()
	id, err := Identify(context.Background(), dir)
	if err != nil {
		t.Fatalf("Identify(%s): %v", dir, err)
	}
	return id
}

func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return r
}

func TestIdentifyRepo(t *testing.T) {
	repo := initRepo(t)
	id := identify(t, repo)

	wantRoot := evalSymlinks(t, repo)
	if id.WorktreeRoot != wantRoot {
		t.Errorf("WorktreeRoot = %q, want %q", id.WorktreeRoot, wantRoot)
	}
	wantWS := evalSymlinks(t, filepath.Join(repo, ".git"))
	if string(id.WorkspaceID) != wantWS {
		t.Errorf("WorkspaceID = %q, want %q", id.WorkspaceID, wantWS)
	}
	if len(id.WorktreeID) != worktreeIDLen {
		t.Errorf("WorktreeID = %q, want %d chars", id.WorktreeID, worktreeIDLen)
	}
	// A subdirectory of the worktree resolves to the same identity.
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := identify(t, sub); got != id {
		t.Errorf("subdir identity = %+v, want %+v", got, id)
	}
}

func TestLinkedWorktreeSharesWorkspace(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-q", wt)

	main := identify(t, repo)
	linked := identify(t, wt)

	if linked.WorkspaceID != main.WorkspaceID {
		t.Errorf("linked WorkspaceID = %q, want shared %q", linked.WorkspaceID, main.WorkspaceID)
	}
	if linked.WorktreeRoot == main.WorktreeRoot {
		t.Errorf("linked WorktreeRoot %q should differ from main", linked.WorktreeRoot)
	}
	if linked.WorktreeID == main.WorktreeID {
		t.Errorf("linked WorktreeID %q should differ from main", linked.WorktreeID)
	}
}

func TestSeparateClonesDiffer(t *testing.T) {
	a := identify(t, initRepo(t))
	b := identify(t, initRepo(t))
	if a.WorkspaceID == b.WorkspaceID {
		t.Errorf("separate repos share WorkspaceID %q", a.WorkspaceID)
	}
}

func TestSymlinkedPathNormalizes(t *testing.T) {
	repo := initRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if got, want := identify(t, link), identify(t, repo); got != want {
		t.Errorf("symlinked identity = %+v, want %+v", got, want)
	}
}

func TestNotGit(t *testing.T) {
	_, err := Identify(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error outside a git repo")
	}
	if !errors.Is(err, ErrNotGit) {
		t.Errorf("error = %v, want ErrNotGit", err)
	}
}
