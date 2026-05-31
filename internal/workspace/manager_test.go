package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestManagerOpenAndCurrent(t *testing.T) {
	repo := initRepo(t)
	m := NewManager("epoch-1")

	if _, err := m.Current(); !errors.Is(err, ErrNoActiveWorkspace) {
		t.Fatalf("Current before Open = %v, want ErrNoActiveWorkspace", err)
	}

	info, err := m.Open(context.Background(), repo)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if info.Ephemeral {
		t.Error("git workspace reported ephemeral")
	}
	if info.DaemonEpoch != "epoch-1" {
		t.Errorf("DaemonEpoch = %q, want epoch-1", info.DaemonEpoch)
	}
	if info.WorktreeRoot != evalSymlinks(t, repo) {
		t.Errorf("WorktreeRoot = %q, want %q", info.WorktreeRoot, evalSymlinks(t, repo))
	}

	cur, err := m.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur != info {
		t.Errorf("Current = %+v, want %+v", cur, info)
	}
}

func TestManagerOpenSwitchesActive(t *testing.T) {
	m := NewManager("e")
	first, err := m.Open(context.Background(), initRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Open(context.Background(), initRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := m.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cur != second {
		t.Errorf("Current = %+v, want most-recently-opened %+v", cur, second)
	}
	// The earlier workspace remains retrievable for mutation gating.
	if w, ok := m.Lookup(first.WorkspaceID); !ok || w.WorkspaceID != first.WorkspaceID {
		t.Errorf("Lookup(first) = %+v, %v", w, ok)
	}
}

func TestManagerOpenEphemeralIsReadOnly(t *testing.T) {
	m := NewManager("e")
	info, err := m.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !info.Ephemeral {
		t.Fatal("non-git workspace not reported ephemeral")
	}
	// A mutating handler would Lookup the workspace and refuse via EnsureMutable.
	w, ok := m.Lookup(info.WorkspaceID)
	if !ok {
		t.Fatal("opened workspace not found by Lookup")
	}
	if err := w.EnsureMutable(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("EnsureMutable = %v, want ErrReadOnly", err)
	}
}

func TestManagerOpenBadDir(t *testing.T) {
	m := NewManager("e")
	if _, err := m.Open(context.Background(), "/no/such/dir/here"); err == nil {
		t.Fatal("Open of nonexistent dir should fail")
	}
}
