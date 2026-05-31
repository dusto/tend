// Package workspace derives a workspace's identity from git. The workspace id
// is the canonical path of the repository's common git dir, so linked worktrees
// of one repository share an id while separate clones do not; each worktree
// additionally has its own root and a stable short id derived from that root.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
)

// worktreeIDLen is the number of hex characters kept from the worktree-root
// hash. 12 hex chars (48 bits) is short enough to log yet collision-resistant
// across a user's set of worktrees.
const worktreeIDLen = 12

// ErrNotGit reports that the directory is not inside a git working tree, so no
// git-based identity can be derived.
var ErrNotGit = errors.New("workspace: not a git working tree")

// ErrReadOnly reports that a mutating operation was attempted on an ephemeral
// (non-git) workspace, which is read-only.
var ErrReadOnly = errors.New("workspace: ephemeral workspace is read-only")

// ephemeralPrefix marks a WorkspaceID derived from a directory outside git.
const ephemeralPrefix = "ephemeral:"

// Identity is a workspace's git-derived identity.
type Identity struct {
	// WorkspaceID is the canonical path of the common git dir. Linked worktrees
	// of one repository share it; separate clones differ.
	WorkspaceID api.WorkspaceID
	// WorktreeRoot is the canonical path of this worktree's top-level directory.
	WorktreeRoot string
	// WorktreeID is a stable short hash of WorktreeRoot.
	WorktreeID api.WorktreeID
}

// Workspace is a resolved directory: its identity plus whether it is ephemeral.
// An ephemeral workspace (a directory outside git) is read-only and exists only
// in memory; nothing it produces is persisted across daemon restarts.
type Workspace struct {
	Identity
	Ephemeral bool
}

// EnsureMutable returns ErrReadOnly when w is ephemeral, and nil otherwise.
// Mutating operations (task, session, file, and pane changes) call it to refuse
// work outside git.
func (w Workspace) EnsureMutable() error {
	if w.Ephemeral {
		return ErrReadOnly
	}
	return nil
}

// Resolve resolves dir to a Workspace. Inside a git working tree it carries the
// git identity (see Identify). Outside git it is an ephemeral, read-only
// workspace whose WorkspaceID is "ephemeral:<canonical-path>".
func Resolve(ctx context.Context, dir string) (Workspace, error) {
	id, err := Identify(ctx, dir)
	if err == nil {
		return Workspace{Identity: id}, nil
	}
	if !errors.Is(err, ErrNotGit) {
		return Workspace{}, err
	}
	root, err := canonical(dir)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{
		Identity: Identity{
			WorkspaceID:  api.WorkspaceID(ephemeralPrefix + root),
			WorktreeRoot: root,
			WorktreeID:   worktreeID(root),
		},
		Ephemeral: true,
	}, nil
}

// Identify derives the workspace identity for dir. It returns ErrNotGit when dir
// is not inside a git working tree. Paths are resolved through symlinks, so a
// symlinked and a real path to the same worktree yield the same identity.
func Identify(ctx context.Context, dir string) (Identity, error) {
	commonDir, err := gitPath(ctx, dir, "--git-common-dir")
	if err != nil {
		return Identity{}, err
	}
	topLevel, err := gitPath(ctx, dir, "--show-toplevel")
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		WorkspaceID:  api.WorkspaceID(commonDir),
		WorktreeRoot: topLevel,
		WorktreeID:   worktreeID(topLevel),
	}, nil
}

// gitPath runs `git rev-parse --path-format=absolute <flag>` in dir and returns
// the result resolved through symlinks.
func gitPath(ctx context.Context, dir, flag string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", flag)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("%w: %s", ErrNotGit, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("workspace: running git: %w", err)
	}
	return canonical(strings.TrimSpace(stdout.String()))
}

// canonical returns the absolute path of p with all symlinks resolved.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("workspace: resolving %s: %w", p, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: resolving %s: %w", abs, err)
	}
	return resolved, nil
}

// worktreeID returns a stable short hash of a canonical worktree root.
func worktreeID(root string) api.WorktreeID {
	sum := sha256.Sum256([]byte(root))
	return api.WorktreeID(hex.EncodeToString(sum[:])[:worktreeIDLen])
}
