package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// ErrDaemonRunning is returned by Listen when a live daemon already owns the
// socket. A second daemon should treat this as "defer to the running instance".
var ErrDaemonRunning = errors.New("rpc: a tend daemon is already listening on the socket")

// SocketPath returns the daemon's Unix socket path: $XDG_RUNTIME_DIR/tend/tend.sock,
// falling back to <tmp>/tend-<uid>/tend.sock when XDG_RUNTIME_DIR is unset.
func SocketPath() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "tend", "tend.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("tend-%d", os.Getuid()), "tend.sock")
}

// Listen prepares and listens on the Unix socket at path. It ensures the parent
// directory exists with 0700 perms, is owned by the current user, and is not a
// symlink; reclaims a stale socket only when it is owned by the current user and
// no live daemon answers; refuses to clobber a live socket (ErrDaemonRunning) or
// any non-socket file; and sets the socket file mode to 0600.
func Listen(path string) (net.Listener, error) {
	if err := ensureRuntimeDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := reclaimStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func ensureRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("rpc: runtime dir %s is a symlink; refusing", dir)
	}
	if err := checkOwnedByUser(fi, dir); err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func reclaimStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("rpc: socket path %s is a symlink; refusing", path)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("rpc: %s exists and is not a socket; refusing to remove", path)
	}
	// A socket exists: is a daemon alive on it?
	if conn, derr := net.DialTimeout("unix", path, 200*time.Millisecond); derr == nil {
		_ = conn.Close()
		return ErrDaemonRunning
	}
	// Stale socket: only reclaim what we own.
	if err := checkOwnedByUser(fi, path); err != nil {
		return err
	}
	return os.Remove(path)
}

func checkOwnedByUser(fi os.FileInfo, name string) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("rpc: cannot determine owner of %s", name)
	}
	if st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("rpc: %s is not owned by the current user; refusing", name)
	}
	return nil
}

// NewEpoch returns a fresh daemon epoch: a random hex string, distinct per call,
// that identifies one daemon process so a restart can be distinguished.
func NewEpoch() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16) // crypto/rand should never fail
	}
	return hex.EncodeToString(b[:])
}
