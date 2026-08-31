// Package files implements the daemon's repo-file tools. file.read is
// editor-aware: when the file is open in the session's bound editor it returns
// the live buffer content and a changedtick base; otherwise it reads disk and
// returns a content-hash base. The daemon owns the disk read and hash so the
// same code that produces a base also verifies it on a later apply.
//
// file.patch and file.write are the mutating tools: session-scoped and
// approval-gated (task association optional; see ADR 0006). Each is a
// single-target change set — the daemon verifies the cited base is still
// current, blocks on the approval gate, and on approval re-verifies the base and
// applies editor-aware (an open buffer through editor.write_buffer, a closed file
// to disk via temp + atomic rename).
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/diff"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/patch"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/worktree"
)

// File method names.
const (
	MethodRead           = "file.read"
	MethodOpen           = "file.open"
	MethodPatch          = "file.patch"
	MethodWrite          = "file.write"
	MethodApplyChangeSet = "file.apply_change_set"
	MethodDiff           = "file.diff"
)

// Errors returned by the file service.
var (
	// ErrNoSession reports that no session has the given id.
	ErrNoSession = errors.New("files: unknown session")
	// ErrBadURI reports a uri that is not a usable file:// reference. It aliases
	// the shared worktree error so the boundary check lives in one place.
	ErrBadURI = worktree.ErrBadURI
	// ErrOutsideWorkspace reports that a uri resolves outside the session's
	// worktree, so it is refused.
	ErrOutsideWorkspace = worktree.ErrOutsideWorkspace
	// ErrAccessDenied reports that a gated outside-worktree read was refused —
	// the user denied the filesystem_access approval, or the resolved target
	// changed between approval and read (a symlink repoint).
	ErrAccessDenied = errors.New("files: filesystem access denied")
)

// editorClient is the slice of editor.Service the file service drives. It is an
// interface so the file tools can be tested without a live editor connection.
type editorClient interface {
	ReadBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error)
	WriteBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorWriteBufferParams) (api.EditorWriteBufferResult, error)
	Open(ctx context.Context, sessionID api.SessionID, p api.EditorOpenParams) (api.EditorOpenResult, error)
}

// approver gates a mutating action on a session. *approvals.Gate satisfies it.
type approver interface {
	Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error)
}

// Options configures a Service.
type Options struct {
	// NewChangeSetID assigns a change-set id; nil uses a random hex generator.
	NewChangeSetID func() api.ChangeSetID
	// RetainChangeSets caps how many recent change sets keep their
	// before/after snapshots per session for file.diff; 0 uses a default.
	RetainChangeSets int
	// ExtraReadableRoots are directories outside the worktree that reads resolve
	// as in-scope WITHOUT prompting (e.g. the module cache). Conservative by
	// default (empty); broad roots are opt-in config, never a default. Writes
	// ignore these — an outside-worktree write stays hard-denied.
	ExtraReadableRoots []string
	// PromptCapable reports whether any connected client can answer an approval.
	// An outside read with no prompt-capable client would block forever on an
	// unanswerable prompt, so it hard-denies instead. Nil means "assume prompting
	// is available" (the daemon always wires it; nil is for tests).
	PromptCapable func() bool
	// Emitter publishes the artifact_written session event after a mutation is
	// applied, so a client can render the result inline. Nil disables it.
	Emitter Emitter
}

// Emitter publishes session events. *events.Store satisfies it.
type Emitter interface {
	Publish(api.Event) (api.Event, error)
}

// defaultRetainChangeSets is the per-session snapshot retention when Options
// does not set one.
const defaultRetainChangeSets = 16

// Service implements the file tools over the session registry, the editor
// reverse-RPC, and the approval gate. It is safe for concurrent use.
type Service struct {
	sessions      *session.Registry
	editors       editorClient
	approver      approver
	newID         func() api.ChangeSetID
	snapshots     *snapshotStore
	extraRoots    []string
	promptCapable func() bool
	emit          Emitter
}

// NewService returns a Service. approver may be nil only if the mutating methods
// are never called.
func NewService(sessions *session.Registry, editors editorClient, gate approver, opts Options) *Service {
	newID := opts.NewChangeSetID
	if newID == nil {
		newID = func() api.ChangeSetID { return api.ChangeSetID(randomID()) }
	}
	retain := opts.RetainChangeSets
	if retain <= 0 {
		retain = defaultRetainChangeSets
	}
	return &Service{
		sessions:      sessions,
		editors:       editors,
		approver:      gate,
		newID:         newID,
		snapshots:     newSnapshotStore(retain),
		extraRoots:    opts.ExtraReadableRoots,
		promptCapable: opts.PromptCapable,
		emit:          opts.Emitter,
	}
}

// maxArtifactContent caps the new-content embedded in an artifact_written event.
// Above it, content is omitted (Truncated set) and a client fetches the file with
// read_buffer, so the event stream is not bloated by a very large write.
const maxArtifactContent = 256 * 1024

// emitArtifact publishes the artifact_written record for an applied mutation, so
// a client can render the result inline. Best-effort: a nil emitter or a publish
// error never fails the write.
func (s *Service) emitArtifact(sessionID api.SessionID, uri string, csid api.ChangeSetID, unified, content string) {
	if s.emit == nil {
		return
	}
	art := api.ArtifactWritten{
		SessionID:   sessionID,
		URI:         uri,
		ChangeSetID: csid,
		Diff:        unified,
	}
	if len(content) > maxArtifactContent {
		art.Truncated = true
	} else {
		art.Content = content
	}
	payload, err := json.Marshal(art)
	if err != nil {
		return
	}
	_, _ = s.emit.Publish(api.Event{
		StreamID: api.SessionStream(sessionID),
		Scope:    api.ScopeSession,
		Type:     "artifact_written",
		Payload:  payload,
	})
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
	path, err := s.resolveRead(ctx, sess, p.URI, api.FilesystemModeRead, MethodRead)
	if err != nil {
		return api.FileReadResult{}, err
	}
	st, err := s.readCurrent(ctx, p.SessionID, p.URI, path)
	if err != nil {
		return api.FileReadResult{}, err
	}
	return api.FileReadResult{Content: string(st.content), Base: st.base(), Open: st.open}, nil
}

// Open asks the session's bound editor to open a repo file in a buffer for the
// user to see. It is non-mutating (it changes editor window
// state, nothing on disk), but the uri is worktree-bounded: an agent for one
// repo cannot make the editor open another's files. A headless session (no bound
// editor) is a no-op — Open is false in the result — rather than an error, so an
// agent that opportunistically opens a file is not penalized when there is no
// editor to receive it.
func (s *Service) Open(ctx context.Context, p api.FileOpenParams) (api.FileOpenResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.FileOpenResult{}, ErrNoSession
	}
	// Worktree-bound the target hard (no outside-read gate): opening is a UI
	// affordance, and an out-of-worktree open is simply refused.
	if _, err := resolvePath(p.URI, sess.WorktreeRoot); err != nil {
		return api.FileOpenResult{}, err
	}
	_, err := s.editors.Open(ctx, p.SessionID, api.EditorOpenParams{URIs: []string{p.URI}})
	if errors.Is(err, editor.ErrEditorUnavailable) {
		return api.FileOpenResult{Open: false}, nil
	}
	if err != nil {
		return api.FileOpenResult{}, err
	}
	return api.FileOpenResult{Open: true}, nil
}

// resolveRead resolves uri to a readable path, gating an outside-worktree read
// on a filesystem_access approval. An in-worktree (or extra-readable-root) path
// resolves and returns as before. An outside path is a consent decision, not a
// hard boundary: it raises a filesystem_access approval and blocks like any
// gated action; on approval it re-resolves and requires the SAME target (a
// symlink repointed between prompt and read is refused — TOCTOU), on denial it
// returns ErrAccessDenied without touching disk or editor. When no approver is
// wired (reads cannot be gated) an outside path stays hard-denied.
func (s *Service) resolveRead(ctx context.Context, sess *session.Session, uri, mode, tool string) (string, error) {
	resolved, inside, err := worktree.ClassifyPath(uri, sess.WorktreeRoot, s.extraRoots...)
	if err != nil {
		return "", err
	}
	if inside {
		return resolved, nil
	}
	// An outside read can only proceed with user consent. If it cannot be gated —
	// no approver wired, or no prompt-capable client to answer (a headless or
	// CLI-only session) — it stays hard-denied rather than blocking forever on an
	// unanswerable prompt (the pre-consent behavior for such sessions).
	if s.approver == nil || (s.promptCapable != nil && !s.promptCapable()) {
		return "", ErrOutsideWorkspace
	}
	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind: api.ApprovalFilesystemAccess,
		FilesystemAccess: &api.FilesystemAccessApproval{
			RequestedURI: uri,
			ResolvedPath: resolved,
			Mode:         mode,
			Tool:         tool,
		},
	})
	outcome, err := s.approver.Request(ctx, sess, api.ApprovalFilesystemAccess, detail)
	if err != nil {
		return "", err
	}
	if !outcome.Approved {
		return "", ErrAccessDenied
	}
	// TOCTOU: re-resolve immediately before access and require the exact target
	// that was approved. A symlink can be repointed between the prompt and the
	// read; the approval was for the resolved target, so a different one is
	// refused rather than silently read.
	reResolved, _, err := worktree.ClassifyPath(uri, sess.WorktreeRoot, s.extraRoots...)
	if err != nil {
		return "", err
	}
	if reResolved != resolved {
		return "", ErrAccessDenied
	}
	return resolved, nil
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
	// No task requirement: the approval gate below is the supervision boundary
	// for a mutation, not task association (ADR 0006).
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
	unified := diff.Unified(string(st.content), string(newContent))
	// Snapshots are captured at proposal time, so the set is reviewable via
	// file.diff even while pending or after a denial.
	s.snapshots.record(csid, sessionID, []changeSetFile{{
		uri: uri, before: string(st.content), after: string(newContent), diff: unified,
	}})
	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind: api.ApprovalFileEdit,
		FileEdit: &api.FileEditApproval{
			ChangeSetID: csid,
			Targets: []api.FileEditTarget{{
				URI:  uri,
				Base: base,
				Diff: unified,
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
	s.snapshots.setApplied(csid, true, map[string]bool{uri: true})
	// The file is durably written: record it as an artifact so a client can render
	// the result inline (diff + new content) rather than only noting a file changed.
	s.emitArtifact(sessionID, uri, csid, unified, string(newContent))
	return api.FileMutationResult{ChangeSetID: csid, Applied: true, Base: newBase}, nil
}

// Diff returns a change set's captured before/after snapshots. Read-only and
// a review affordance that only surfaces what the named
// proposal or applied set changed.
func (s *Service) Diff(_ context.Context, p api.FileDiffParams) (api.FileDiffResult, error) {
	res, ok := s.snapshots.get(p.ChangeSetID)
	if !ok {
		return api.FileDiffResult{}, ErrUnknownChangeSet
	}
	return res, nil
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

// resolvePath converts a file:// uri to a filesystem path within the session
// worktree (see internal/worktree for the boundary and symlink-escape rules).
func resolvePath(uri, worktreeRoot string) (string, error) {
	return worktree.ResolvePath(uri, worktreeRoot)
}
