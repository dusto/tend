package pty

import (
	"bytes"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const shell = "/bin/sh"

func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pane did not finish in time")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestSpawnCapturesScrollbackAndExits(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "printf hello-pane"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitClosed(t, p.Done())

	if p.Running() {
		t.Error("pane should not be running after exit")
	}
	if code, exited := p.ExitCode(); !exited || code != 0 {
		t.Errorf("exit = (%d, %v), want (0, true)", code, exited)
	}
	if !bytes.Contains(p.Scrollback(), []byte("hello-pane")) {
		t.Errorf("scrollback = %q, want it to contain hello-pane", p.Scrollback())
	}
}

func TestSpawnNonZeroExit(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitClosed(t, p.Done())
	if code, _ := p.ExitCode(); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestWriteIsEchoedAndClose(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "cat"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, err := p.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// cat (and the PTY echo) reflect the input back into the scrollback.
	waitFor(t, func() bool { return bytes.Contains(p.Scrollback(), []byte("ping")) })

	if !p.Running() {
		t.Error("cat should still be running")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if p.Running() {
		t.Error("pane should be stopped after Close")
	}
}

func TestCloseKillsLongRunningProcess(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !p.Running() {
		t.Fatal("sleep should be running")
	}
	_ = p.Close()
	waitClosed(t, p.Done())
	if _, exited := p.ExitCode(); !exited {
		t.Error("pane should be exited after Close")
	}
}

func TestSubscribeStreamsLiveOutputThenCloses(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "cat"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	ch, cancel := p.Subscribe()
	defer cancel()

	if _, err := p.Write([]byte("stream-me\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got bytes.Buffer
	deadline := time.After(5 * time.Second)
	for !bytes.Contains(got.Bytes(), []byte("stream-me")) {
		select {
		case chunk := <-ch:
			got.Write(chunk)
		case <-deadline:
			t.Fatalf("did not receive streamed output; got %q", got.Bytes())
		}
	}

	// Closing the process closes the subscription channel.
	_ = p.Close()
	waitClosed(t, p.Done())
	waitFor(t, func() bool {
		select {
		case _, ok := <-ch:
			return !ok
		default:
			return false
		}
	})
}

func TestSubscribeAfterExitIsClosed(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "true"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitClosed(t, p.Done())
	ch, cancel := p.Subscribe()
	defer cancel()
	if _, ok := <-ch; ok {
		t.Error("subscribing after exit should yield a closed channel")
	}
}

// alive reports whether pid names a live process (signal 0 probes existence).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestCloseTerminatesDescendants(t *testing.T) {
	// A shell that backgrounds a long sleep and prints its pid: closing the pane
	// must kill the whole group, not just the shell, so the sleep does not survive
	// as an orphan outside daemon ownership.
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 30 & echo CHILD=$!; wait"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	re := regexp.MustCompile(`CHILD=(\d+)`)
	var child int
	waitFor(t, func() bool {
		m := re.FindSubmatch(p.Scrollback())
		if m == nil {
			return false
		}
		child, _ = strconv.Atoi(string(m[1]))
		return child > 0
	})
	if !alive(child) {
		t.Fatalf("child %d should be running before close", child)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, func() bool { return !alive(child) })
}

func TestResize(t *testing.T) {
	p, err := spawn(SpawnConfig{Command: shell, Args: []string{"-c", "sleep 60"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = p.Close() }()
	if err := p.Resize(40, 100); err != nil {
		t.Errorf("resize: %v", err)
	}
}
