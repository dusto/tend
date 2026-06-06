// Package pty implements daemon-owned PTYs. The daemon spawns a process under a
// pseudo-terminal it owns, captures the full output as scrollback, and keeps the
// process running independently of any visible terminal view — so a build or
// test run survives editor or pane-view detach and stays readable. The visible
// terminal bridge and the pane.* wire methods are layered on separately.
package pty

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"

	"github.com/dusto/tend/api"
)

// defaultScrollback is the per-pane scrollback cap in bytes. Older output is
// dropped once a pane produces more than this.
const defaultScrollback = 1 << 20 // 1 MiB

// Default terminal size for a pane whose caller did not specify one.
const (
	defaultRows = 24
	defaultCols = 80
)

// SpawnConfig describes a pane's process and its terminal size.
type SpawnConfig struct {
	Command      string // executable (e.g. a shell)
	Args         []string
	Env          []string // nil inherits the daemon's environment (os/exec rules)
	Dir          string   // working directory
	Workspace    api.WorkspaceID
	WorktreeRoot string
	Rows, Cols   uint16 // initial size; 0 leaves the PTY default
}

// Pane is a daemon-owned PTY and the process running under it. It is safe for
// concurrent use.
type Pane struct {
	ID           api.PaneID
	Workspace    api.WorkspaceID
	WorktreeRoot string
	Cwd          string

	ptmx *os.File
	cmd  *exec.Cmd

	mu       sync.Mutex
	scroll   *ring
	exited   bool
	exitCode int
	subs     map[chan []byte]struct{}

	done chan struct{}
}

// spawn starts cfg's process under a new PTY and begins capturing output. It is
// used by the Manager. beforeCapture, if non-nil, runs after the pane is built
// but before output capture begins, so a caller can attach a subscriber that is
// guaranteed to see the pane's output from its very first byte.
func spawn(cfg SpawnConfig, beforeCapture func(*Pane)) (*Pane, error) {
	if cfg.Command == "" {
		return nil, errors.New("pty: command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = cfg.Env
	cmd.Dir = cfg.Dir

	rows, cols := cfg.Rows, cfg.Cols
	if rows == 0 {
		rows = defaultRows
	}
	if cols == 0 {
		cols = defaultCols
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}

	p := &Pane{
		ID:           api.PaneID(randomID()),
		Workspace:    cfg.Workspace,
		WorktreeRoot: cfg.WorktreeRoot,
		Cwd:          cfg.Dir,
		ptmx:         ptmx,
		cmd:          cmd,
		scroll:       newRing(defaultScrollback),
		subs:         make(map[chan []byte]struct{}),
		done:         make(chan struct{}),
	}
	if beforeCapture != nil {
		beforeCapture(p)
	}
	go p.capture()
	return p, nil
}

// capture reads PTY output into the scrollback ring and fans it out to
// subscribers until the PTY closes (the process exited or was killed), then
// reaps the process and marks the pane exited.
func (p *Pane) capture() {
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.deliver(buf[:n])
		}
		if err != nil {
			break
		}
	}
	code := 0
	if err := p.cmd.Wait(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}
	p.mu.Lock()
	p.exited = true
	p.exitCode = code
	for ch := range p.subs {
		close(ch)
		delete(p.subs, ch)
	}
	p.mu.Unlock()
	_ = p.ptmx.Close()
	close(p.done)
}

// deliver appends output to the scrollback and broadcasts it to subscribers
// (best-effort: a subscriber that is not draining drops the chunk).
func (p *Pane) deliver(b []byte) {
	chunk := append([]byte(nil), b...)
	p.mu.Lock()
	p.scroll.write(chunk)
	for ch := range p.subs {
		select {
		case ch <- chunk:
		default:
		}
	}
	p.mu.Unlock()
}

// Write sends input to the pane's process via the PTY.
func (p *Pane) Write(b []byte) (int, error) { return p.ptmx.Write(b) }

// Resize propagates a terminal size change to the PTY.
func (p *Pane) Resize(rows, cols uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Scrollback returns a copy of the captured output (capped; oldest bytes dropped
// past the cap).
func (p *Pane) Scrollback() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scroll.bytes()
}

// Subscribe returns a channel of new output chunks and a cancel function. The
// channel closes when the pane exits. Subscribers see only output produced after
// subscribing; use Scrollback for history. Delivery is best-effort.
func (p *Pane) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	p.subs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			p.mu.Lock()
			if _, ok := p.subs[ch]; ok {
				delete(p.subs, ch)
				close(ch)
			}
			p.mu.Unlock()
		})
	}
	return ch, cancel
}

// Done is closed when the process has exited (or been killed) and reaped.
func (p *Pane) Done() <-chan struct{} { return p.done }

// Running reports whether the process is still running.
func (p *Pane) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.exited
}

// ExitCode returns the process exit code and whether it has exited.
func (p *Pane) ExitCode() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, p.exited
}

// Close terminates the pane's process and releases the PTY. It is idempotent and
// blocks until the process is reaped. Because the PTY starts the command as a
// session/group leader, Close signals the whole process group, so descendant
// processes (a shell's children, a build's subprocesses) are terminated too
// rather than orphaned outside daemon ownership.
func (p *Pane) Close() error {
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if proc := p.cmd.Process; proc != nil && !exited {
		// The process is the leader of its own group (pgid == pid), so the
		// negative pid targets the whole group. Signalling only while it is still
		// unreaped avoids racing the pid into a reused, unrelated group.
		if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
			_ = proc.Kill() // fall back to the leader if the group signal failed
		}
	}
	_ = p.ptmx.Close()
	<-p.done
	return nil
}

// randomID returns an unguessable pane id.
func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
