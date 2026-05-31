package rpc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got, want := SocketPath(), "/run/user/1000/tend/tend.sock"; got != want {
		t.Fatalf("XDG path: got %q, want %q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := SocketPath(); !strings.HasSuffix(got, "/tend.sock") || !strings.Contains(got, "tend-") {
		t.Fatalf("fallback path looks wrong: %q", got)
	}
}

func TestListenCreatesSocketWithPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rt")
	path := filepath.Join(dir, "tend.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	si, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if si.Mode()&os.ModeSocket == 0 {
		t.Fatalf("not a socket: %v", si.Mode())
	}
	if perm := si.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket perm = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm = %o, want 700", perm)
	}
}

func TestListenLiveSocketRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tend.sock")
	ln1, err := Listen(path)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer func() { _ = ln1.Close() }()

	if _, err := Listen(path); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second Listen: got %v, want ErrDaemonRunning", err)
	}
}

func TestListenReclaimsStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tend.sock")
	// Create a socket file with no live listener: listen, then close while
	// leaving the file behind.
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("seed listen: %v", err)
	}
	ul.SetUnlinkOnClose(false)
	_ = ul.Close() // file remains, nobody listening -> stale

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket should exist: %v", err)
	}
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen should reclaim stale socket: %v", err)
	}
	defer func() { _ = ln.Close() }()
}

func TestListenRefusesNonSocketFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tend.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("expected refusal to clobber a non-socket file")
	}
}

func TestListenRefusesSymlinkParent(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "tend.sock")
	_, err := Listen(path)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func TestNewEpochUniqueNonEmpty(t *testing.T) {
	a, b := NewEpoch(), NewEpoch()
	if a == "" || b == "" {
		t.Fatal("epoch must be non-empty")
	}
	if a == b {
		t.Fatal("epochs must differ")
	}
	if len(a) != 32 {
		t.Fatalf("epoch hex length = %d, want 32", len(a))
	}
}
