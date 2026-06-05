// Package files implements the daemon's repo-file tools. file.read is
// editor-aware: when the file is open in the session's bound editor it returns
// the live buffer content and a changedtick base; otherwise it reads disk and
// returns a content-hash base. The daemon owns the disk read and hash so the
// same code that produces a base also verifies it on a later apply.
//
// file.patch and file.write are the mutating tools: task-bound (they require a
// session) and approval-gated. Each is a single-target change set — the daemon
// verifies the cited base is still current, blocks on the approval gate, and on
// approval re-verifies the base and applies editor-aware (an open buffer through
// editor.write_buffer, a closed file to disk via temp + atomic rename).
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/diff"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/patch"
	"github.com/dusto/tend/internal/session"
)

// File method names.
const (
	MethodRead  = "file.read"
	MethodPatch = "file.patch"
	MethodWrite = "file.write"
)

// Errors returned by the file service.
var (
	// ErrNoSession reports that no session has the given id.
	ErrNoSession = errors.New("files: unknown session")
	// ErrBadURI reports a uri that is not a usable file:// reference.
	ErrBadURI = errors.New("files: uri is not a file path")
	// ErrOutsideWorkspace reports that a uri resolves outside the session's
	// worktree, so it is refused.
	ErrOutsideWorkspace = errors.New("files: path is outside the session worktree")
)

// editorClient is the slice of editor.Service the file service drives. It is an
// interface so the file tools can be tested without a live editor connection.
type editorClient interface {
	ReadBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error)
	WriteBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorWriteBufferParams) (api.EditorWriteBufferResult, error)
}

// approver gates a mutating action on a session. *approvals.Gate satisfies it.
type approver interface {
	Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error)
}

// Options configures a Service.
type Options struct {
	// NewChangeSetID assigns a change-set id; nil uses a random hex generator.
	NewChangeSetID func() api.ChangeSetID
}

// Service implements the file tools over the session registry, the editor
// reverse-RPC, and the approval gate. It is safe for concurrent use.
type Service struct {
	sessions *session.Registry
	editors  editorClient
	approver approver
	newID    func() api.ChangeSetID
}

// NewService returns a Service. approver may be nil only if the mutating methods
// are never called.
func NewService(sessions *session.Registry, editors editorClient, gate approver, opts Options) *Service {
	newID := opts.NewChangeSetID
	if newID == nil {
		newID = func() api.ChangeSetID { return api.ChangeSetID(randomID()) }
	}
	return &Service{sessions: sessions, editors: editors, approver: gate, newID: newID}
}

// fileState is a file's current content and source: an open editor buffer (with
// its changedtick) or a closed file on disk.
type fileState struct {
	content []byte
	open    bool
	tick    *int64
}

// Read returns the content of a repo file and the base revision it reflects (see
// the package doc for the editor-aware rule).
func (s *Service) Read(ctx context.Context, p api.FileReadParams) (api.FileReadResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.FileReadResult{}, ErrNoSession
	}
	path, err := resolvePath(p.URI, sess.WorktreeRoot)
	if err != nil {
		return api.FileReadResult{}, err
	}
	st, err := s.readCurrent(ctx, p.SessionID, p.URI, path)
	if err != nil {
		return api.FileReadResult{}, err
	}
	return api.FileReadResult{Content: string(st.content), Base: st.base(), Open: st.open}, nil
}

// Patch applies a set of text edits to a repo file through the change-set flow.
func (s *Service) Patch(ctx context.Context, p api.FilePatchParams) (api.FileMutationResult, error) {
	return s.mutate(ctx, p.SessionID, p.URI, p.Base, func(cur []byte) ([]byte, error) {
		return patch.Apply(cur, p.Edits)
	})
}

// Write replaces a repo file's whole content through the change-set flow.
func (s *Service) Write(ctx context.Context, p api.FileWriteParams) (api.FileMutationResult, error) {
	content := []byte(p.Content)
	return s.mutate(ctx, p.SessionID, p.URI, p.Base, func([]byte) ([]byte, error) {
		return content, nil
	})
}

// mutate runs the single-target change-set flow: preflight base check, approval
// gate, freshness re-check on approval, then an editor-aware apply. transform
// produces the new content from the current content.
func (s *Service) mutate(ctx context.Context, sessionID api.SessionID, uri string, base api.FileBase, transform func([]byte) ([]byte, error)) (api.FileMutationResult, error) {
	sess, ok := s.sessions.Get(sessionID)
	if !ok {
		return api.FileMutationResult{}, ErrNoSession
	}
	path, err := resolvePath(uri, sess.WorktreeRoot)
	if err != nil {
		return api.FileMutationResult{}, err
	}

	// Preflight: the cited base must still be current, and the transform must
	// produce valid content, before we ask for approval.
	st, err := s.readCurrent(ctx, sessionID, uri, path)
	if err != nil {
		return api.FileMutationResult{}, err
	}
	if err := verifyBase(base, st); err != nil {
		return api.FileMutationResult{}, err
	}
	newContent, err := transform(st.content)
	if err != nil {
		return api.FileMutationResult{}, err
	}

	csid := s.newID()
	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind: api.ApprovalFileEdit,
		FileEdit: &api.FileEditApproval{
			ChangeSetID: csid,
			Targets: []api.FileEditTarget{{
				URI:  uri,
				Base: base,
				Diff: diff.Unified(string(st.content), string(newContent)),
			}},
		},
	})
	outcome, err := s.approver.Request(ctx, sess, api.ApprovalFileEdit, detail)
	if err != nil {
		return api.FileMutationResult{}, err
	}
	if !outcome.Approved {
		reason := outcome.Reason
		if reason == "" {
			reason = "denied"
		}
		return api.FileMutationResult{ChangeSetID: csid, Applied: false, Reason: reason}, nil
	}

	// Freshness: re-verify the base is still current before writing, so an edit
	// to the file between proposal and approval is caught rather than overwritten.
	st2, err := s.readCurrent(ctx, sessionID, uri, path)
	if err != nil {
		return api.FileMutationResult{}, err
	}
	if err := verifyBase(base, st2); err != nil {
		return api.FileMutationResult{}, err
	}
	newBase, err := s.apply(ctx, sessionID, uri, path, st2, newContent)
	if err != nil {
		return api.FileMutationResult{}, err
	}
	return api.FileMutationResult{ChangeSetID: csid, Applied: true, Base: newBase}, nil
}

// readCurrent returns the file's current state, preferring the editor's live
// buffer when the file is open there and falling back to disk otherwise.
func (s *Service) readCurrent(ctx context.Context, sessionID api.SessionID, uri, path string) (fileState, error) {
	rb, err := s.editors.ReadBuffer(ctx, sessionID, api.EditorReadBufferParams{URI: uri})
	switch {
	case err == nil && rb.Open:
		return fileState{content: []byte(rb.Content), open: true, tick: rb.Base.ChangedTick}, nil
	case err != nil && !errors.Is(err, editor.ErrEditorUnavailable):
		return fileState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{}, fmt.Errorf("files: reading %s: %w", uri, err)
	}
	return fileState{content: data, open: false}, nil
}

// apply writes newContent to its source: an open buffer through the editor, a
// closed file to disk via temp + atomic rename. It returns the new base.
func (s *Service) apply(ctx context.Context, sessionID api.SessionID, uri, path string, st fileState, newContent []byte) (api.FileBase, error) {
	if st.open {
		res, err := s.editors.WriteBuffer(ctx, sessionID, api.EditorWriteBufferParams{
			URI:     uri,
			Content: string(newContent),
			Base:    api.FileBase{ChangedTick: st.tick},
		})
		if err != nil {
			return api.FileBase{}, err
		}
		return res.Base, nil
	}
	if err := writeFileAtomic(path, newContent); err != nil {
		return api.FileBase{}, err
	}
	return api.FileBase{ContentHash: patch.ContentHash(newContent)}, nil
}

// base returns the FileBase describing this state: a changedtick for an open
// buffer, a content hash for a closed file.
func (st fileState) base() api.FileBase {
	if st.open {
		return api.FileBase{ChangedTick: st.tick}
	}
	return api.FileBase{ContentHash: patch.ContentHash(st.content)}
}

// verifyBase reports a conflict when the cited base no longer matches the
// current state. The base must match the same source it was read from: a
// changedtick for an open buffer, a content hash for a closed file. A
// source-type change (open<->closed) between read and apply is itself a conflict.
func verifyBase(base api.FileBase, st fileState) error {
	if st.open {
		if base.ChangedTick == nil || st.tick == nil || *base.ChangedTick != *st.tick {
			return patch.ErrConflict
		}
		return nil
	}
	if base.ContentHash == "" {
		return patch.ErrConflict
	}
	return patch.VerifyContentHash(st.content, base.ContentHash)
}

// writeFileAtomic writes data to path via a temp file in the same directory and
// an atomic rename, preserving the existing file's mode.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tend-write-*")
	if err != nil {
		return fmt.Errorf("files: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("files: writing temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("files: closing temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("files: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("files: rename: %w", err)
	}
	return nil
}

// resolvePath converts a file:// uri to a filesystem path and verifies it falls
// within the worktree root, refusing traversal outside it. Symlinks are resolved
// before the check, so a link inside the worktree that points outside cannot
// escape it.
func resolvePath(uri, worktreeRoot string) (string, error) {
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
