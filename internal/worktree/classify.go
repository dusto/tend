package worktree

import (
	"net/url"
	"path/filepath"
)

// ClassifyPath resolves a file:// uri to a filesystem path (symlinks resolved)
// and reports whether it falls within worktreeRoot or any of extraRoots. Unlike
// ResolvePath, an outside path is a valid, non-error result (inside=false): it
// is the caller's decision — refuse, or ask for consent — rather than a boundary
// error. Only a malformed uri returns an error (ErrBadURI).
//
// Symlinks are resolved before classification, so a link inside the worktree
// that points outside is classified as outside (its resolved target escapes) —
// a link cannot disguise an outside path as inside. The returned path is the
// resolved target, which is what a consent decision is about.
//
// ResolvePath stays the hard-deny helper: writes and any never-escape caller
// keep calling it and are refused by construction. ClassifyPath is only for
// callers that can offer consent for an outside path.
func ClassifyPath(uri, worktreeRoot string, extraRoots ...string) (resolved string, inside bool, err error) {
	u, perr := url.Parse(uri)
	if perr != nil || u.Scheme != "file" || u.Path == "" {
		return "", false, ErrBadURI
	}
	path := resolveSymlinks(filepath.Clean(u.Path))
	if within(resolveSymlinks(filepath.Clean(worktreeRoot)), path) {
		return path, true, nil
	}
	for _, extra := range extraRoots {
		if extra == "" {
			continue
		}
		if within(resolveSymlinks(filepath.Clean(extra)), path) {
			return path, true, nil
		}
	}
	return path, false, nil
}
