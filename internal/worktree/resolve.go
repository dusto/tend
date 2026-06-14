// Package worktree resolves file:// URIs against a session's worktree root and
// enforces the worktree boundary: a URI must name a file inside the worktree,
// with symlinks resolved first so a link inside the worktree cannot escape it.
// It is shared by every tool that turns an agent-supplied URI into a path, so
// the boundary check is defined once.
package worktree

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// Errors returned by ResolvePath.
var (
	// ErrBadURI reports a uri that is not a usable file:// reference.
	ErrBadURI = errors.New("worktree: uri is not a file path")
	// ErrOutsideWorkspace reports that a uri resolves outside the worktree, so
	// it is refused.
	ErrOutsideWorkspace = errors.New("worktree: path is outside the session worktree")
)

// ResolvePath converts a file:// uri to a filesystem path and verifies it falls
// within the worktree root, refusing traversal outside it. Symlinks are
// resolved before the check, so a link inside the worktree that points outside
// cannot escape it.
func ResolvePath(uri, worktreeRoot string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", ErrBadURI
	}
	root := resolveSymlinks(filepath.Clean(worktreeRoot))
	path := resolveSymlinks(filepath.Clean(u.Path))
	if !within(root, path) {
		return "", ErrOutsideWorkspace
	}
	return path, nil
}

// Contains reports whether uri names a file inside worktreeRoot, with symlinks
// resolved. Unlike ResolvePath it does not say why not — use it where an
// out-of-worktree path is simply out of scope rather than a refused request.
func Contains(uri, worktreeRoot string) bool {
	_, err := ResolvePath(uri, worktreeRoot)
	return err == nil
}

// resolveSymlinks returns path with symlinks resolved. When the leaf does not
// exist yet (so EvalSymlinks fails), it resolves the longest existing ancestor
// and re-appends the remaining components, so a symlinked parent still cannot
// disguise a path that escapes the worktree.
func resolveSymlinks(path string) string {
	suffix := ""
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path // nothing along the path resolves
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}

// within reports whether path is root itself or lies beneath it.
func within(root, path string) bool {
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}
