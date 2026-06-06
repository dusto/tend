package pty

import "testing"

func TestManagerSpawnGetList(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Shutdown)

	p, err := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}, Workspace: "ws1"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got, ok := m.Get(p.ID); !ok || got != p {
		t.Fatalf("Get = %v, %v", got, ok)
	}
	if p.Workspace != "ws1" {
		t.Errorf("workspace = %q", p.Workspace)
	}
	if len(m.List()) != 1 {
		t.Errorf("list len = %d, want 1", len(m.List()))
	}
}

func TestManagerCloseRemoves(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Shutdown)
	p, _ := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}})

	if !m.Close(p.ID) {
		t.Fatal("Close should report it closed the pane")
	}
	if _, ok := m.Get(p.ID); ok {
		t.Error("pane should be gone after Close")
	}
	waitClosed(t, p.Done())
	if m.Close(p.ID) {
		t.Error("closing an unknown pane should report false")
	}
}

func TestManagerShutdownClosesAll(t *testing.T) {
	m := NewManager()
	p1, _ := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}})
	p2, _ := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}})

	m.Shutdown()
	waitClosed(t, p1.Done())
	waitClosed(t, p2.Done())
	if len(m.List()) != 0 {
		t.Errorf("list len = %d after shutdown, want 0", len(m.List()))
	}
}

func TestManagerUnknownPaneIDs(t *testing.T) {
	m := NewManager()
	if p, ok := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "true"}}); ok != nil || p.ID == "" {
		t.Fatalf("spawn: %v", ok)
	}
	// Two panes get distinct, non-empty ids.
	a, _ := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "true"}})
	b, _ := m.Spawn(SpawnConfig{Command: shell, Args: []string{"-c", "true"}})
	t.Cleanup(m.Shutdown)
	if a.ID == b.ID || a.ID == "" {
		t.Errorf("pane ids not distinct: %q %q", a.ID, b.ID)
	}
}
