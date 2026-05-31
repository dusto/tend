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
	path := strings.TrimSpace(stdout.String())
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("workspace: resolving %s: %w", path, err)
	}
	return resolved, nil
}

// worktreeID returns a stable short hash of a canonical worktree root.
func worktreeID(root string) api.WorktreeID {
	sum := sha256.Sum256([]byte(root))
	return api.WorktreeID(hex.EncodeToString(sum[:])[:worktreeIDLen])
}
