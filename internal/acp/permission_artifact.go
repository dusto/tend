package acp

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/diff"
	"github.com/dusto/tend/internal/session"
)

// maxArtifactContent caps the new-content embedded in an artifact_written event
// (mirrors the files service); above it the content is omitted (Truncated set).
const maxArtifactContent = 256 * 1024

// emitArtifact publishes an artifact_written record for an approved NATIVE
// file-write tool — Claude's own Write/Edit/MultiEdit — so a client renders the
// result inline exactly as it does for a tend editor-tool write, even though the
// agent performs this write itself. Best-effort: an unrecognised tool, a path
// outside the worktree, or a read/publish error is silently skipped and never
// affects the turn.
func (r *PermissionRouter) emitArtifact(sess *session.Session, toolCallID string, rawInput json.RawMessage) {
	if r.emit == nil || len(rawInput) == 0 {
		return
	}
	path, newContent, ok := writeResult(rawInput, sess.WorktreeRoot)
	if !ok {
		return
	}
	// Read the current (pre-write) content for the diff; empty for a new file.
	old, _ := os.ReadFile(path)

	art := api.ArtifactWritten{
		SessionID: sess.ID,
		URI:       "file://" + path,
		Diff:      diff.Unified(string(old), newContent),
	}
	if len(newContent) > maxArtifactContent {
		art.Truncated = true
	} else {
		art.Content = newContent
	}
	payload, err := json.Marshal(art)
	if err != nil {
		return
	}
	if _, err := r.emit.Publish(api.Event{
		StreamID: api.SessionStream(sess.ID),
		Scope:    api.ScopeSession,
		Type:     "artifact_written",
		Payload:  payload,
	}); err != nil {
		slog.Warn("acp: emit artifact_written failed", "session", sess.ID, "tool_call", toolCallID, "err", err)
	}
}

// writeResult inspects a native tool's raw input and, for a file write it
// recognises by shape (Write = content; Edit = old_string/new_string; MultiEdit =
// edits[]), returns the resolved absolute path (bounded to worktreeRoot) and the
// file's resulting content. ok is false for a non-write tool, a path outside the
// worktree, or an edit whose current content cannot be read.
func writeResult(rawInput json.RawMessage, worktreeRoot string) (path, content string, ok bool) {
	var in struct {
		FilePath   string  `json:"file_path"`
		Content    *string `json:"content"`
		OldString  *string `json:"old_string"`
		NewString  *string `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
		Edits      []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if json.Unmarshal(rawInput, &in) != nil || in.FilePath == "" {
		return "", "", false
	}
	path, ok = resolveInWorktree(in.FilePath, worktreeRoot)
	if !ok {
		return "", "", false
	}
	switch {
	case in.Content != nil: // Write: the full new file content is given.
		return path, *in.Content, true
	case len(in.Edits) > 0: // MultiEdit: apply the sequence to the current content.
		cur, err := os.ReadFile(path)
		if err != nil {
			return "", "", false
		}
		out := string(cur)
		for _, e := range in.Edits {
			out = applyReplace(out, e.OldString, e.NewString, e.ReplaceAll)
		}
		return path, out, true
	case in.OldString != nil && in.NewString != nil: // Edit: one replacement.
		cur, err := os.ReadFile(path)
		if err != nil {
			return "", "", false
		}
		return path, applyReplace(string(cur), *in.OldString, *in.NewString, in.ReplaceAll), true
	}
	return "", "", false
}

// applyReplace mirrors the agent's edit semantics: replace the first occurrence
// of old with new, or all occurrences when replaceAll is set. An empty old is a
// no-op (an empty match would otherwise splice new everywhere).
func applyReplace(s, old, new string, replaceAll bool) string {
	if old == "" {
		return s
	}
	if replaceAll {
		return strings.ReplaceAll(s, old, new)
	}
	return strings.Replace(s, old, new, 1)
}

// resolveInWorktree resolves filePath (absolute, or relative to worktreeRoot) and
// confirms it stays within the worktree — so an artifact read never escapes it.
func resolveInWorktree(filePath, worktreeRoot string) (string, bool) {
	if worktreeRoot == "" {
		return "", false
	}
	p := filePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktreeRoot, p)
	}
	p = filepath.Clean(p)
	rel, err := filepath.Rel(worktreeRoot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}
